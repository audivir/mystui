package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

const macMain = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"

func press(m model, k string) (model, tea.Cmd) {
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	return next.(model), cmd
}

func TestWriteFiles(t *testing.T) {
	projectDir = filepath.Join(t.TempDir(), "proj")
	if err := writeFiles(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"watchdog.sh", "mystvpn-wrapper.sh", "killswitch.sh"} {
		// the Dockerfile copies the scripts from its build context, so they must be written next to it.
		if _, err := os.Stat(filepath.Join(projectDir, "docker", name)); err != nil {
			t.Fatalf("%s not written: %v", name, err)
		}
	}
	for _, name := range []string{"Dockerfile", "compose.yaml"} {
		want, _ := dockerFiles.ReadFile("docker/" + name)
		got, err := os.ReadFile(filepath.Join(projectDir, "docker", name))
		if err != nil || string(got) != string(want) {
			t.Fatalf("%s not written from the embedded copy: %v", name, err)
		}
	}
	if info, err := os.Stat(filepath.Join(projectDir, "data")); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory must exist and stay private: %v", err)
	}
	stale := filepath.Join(projectDir, "docker", "compose.yaml")
	_ = os.WriteFile(stale, []byte("stale"), 0o644)
	if err := writeFiles(); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(stale); string(b) == "stale" {
		t.Fatal("files from an older version must be replaced")
	}
}

func TestXDGDir(t *testing.T) {
	home = "/home/x"
	t.Setenv("XDG_DATA_HOME", "")
	if got := xdgDir("XDG_DATA_HOME", ".local/share"); got != "/home/x/.local/share" {
		t.Fatalf("unset variable must fall back below home, got %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "relative/path")
	if got := xdgDir("XDG_DATA_HOME", ".local/share"); got != "/home/x/.local/share" {
		t.Fatalf("relative values must be ignored, got %q", got)
	}
	t.Setenv("XDG_DATA_HOME", "/srv/data")
	if got := xdgDir("XDG_DATA_HOME", ".local/share"); got != "/srv/data" {
		t.Fatalf("absolute values must win, got %q", got)
	}
}

func TestTypeMemory(t *testing.T) {
	projectDir = t.TempDir()
	if loadType("DE") != "" {
		t.Fatal("nothing saved yet")
	}
	saveType("hosting", "DE")
	if loadType("DE") != "hosting" || loadType("FR") != "" {
		t.Fatal("type must match its country only")
	}
	saveType("", "")
	if loadType("DE") != "" {
		t.Fatal("should be cleared")
	}
}

func TestMarkerOnlyInMatchingList(t *testing.T) {
	useFlags = false
	st := vpn{containerUp: true, connected: true, country: "DE", ipType: "hosting"}
	for i, want := range []bool{false, true} { // residential, hosting
		m := model{w: 80, h: 30, codes: []string{"DE", "FR"}, ipType: i, st: st}
		if got := strings.Contains(m.View(), "●"); got != want {
			t.Errorf("%s list: marker=%v want %v", ipTypes[i], got, want)
		}
	}
	st.ipType = ""
	if strings.Contains((model{w: 80, h: 30, codes: []string{"DE"}, st: st}).View(), "●") {
		t.Error("unknown type must not mark any list")
	}
}

func TestFlag(t *testing.T) {
	useFlags = true
	if flag("DE") != "🇩🇪" || flag("D") != "" {
		t.Fatal("flag conversion")
	}
	useFlags = false
	if flag("DE") != "" {
		t.Fatal("flags off")
	}
}

func TestDetectChrome(t *testing.T) {
	bundled := "/Users/x/.local/share/mystui/chrome"
	normal := "/Users/x/Library/Application Support/Google/Chrome"
	lines := []string{
		macMain + " --user-data-dir=" + bundled + " --proxy-server=socks5://127.0.0.1:1080",
		macMain + " Helper --type=renderer --user-data-dir=" + normal,
		"/x/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper --type=gpu-process",
		"/usr/bin/vim notes.txt",
	}
	if !runningIn(lines, bundled, false) {
		t.Error("bundled profile should be running")
	}
	if runningIn(lines, normal, true) {
		t.Error("normal profile is not running: only helpers and a bundled browser exist")
	}
	if !runningIn([]string{macMain}, normal, true) || runningIn([]string{macMain}, bundled, false) {
		t.Error("a launch without --user-data-dir is the normal profile")
	}
	if !runningIn([]string{"/usr/bin/google-chrome-stable --user-data-dir=/tmp/a b --x"}, "/tmp/a b", false) {
		t.Error("paths with spaces must match")
	}
}

func TestAskWhenChromeRunning(t *testing.T) {
	defer func(f func(string) bool) { chromeRunningFn = f }(chromeRunningFn)
	chromeRunningFn = func(string) bool { return true }
	m, cmd := press(model{w: 100, h: 30}, "c")
	if cmd != nil || m.prompt != "separate" || !strings.Contains(m.View(), "already running") {
		t.Fatalf("expected the separate-profile prompt, got %q", m.prompt)
	}
	if m, cmd = press(m, "n"); cmd != nil || m.confirm != "" || m.msg != "cancelled" {
		t.Fatal("any other key cancels without launching")
	}
	m, _ = press(m, "c")
	if _, cmd = press(m, "y"); cmd == nil {
		t.Fatal("y launches the separate profile")
	}
	chromeRunningFn = func(string) bool { return false }
	if m, cmd = press(model{}, "c"); cmd == nil || m.prompt != "" {
		t.Fatal("not running: launch directly")
	}
}

func TestClearChrome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chrome")
	_ = os.MkdirAll(filepath.Join(dir, "Default"), 0o700)
	_ = os.WriteFile(filepath.Join(dir, "Default", "Cookies"), make([]byte, 3<<20), 0o600)
	bundledProfile = dir

	m, _ := press(model{w: 100, h: 30}, "C")
	if !strings.Contains(m.confirm, "3.0 MB") {
		t.Fatalf("prompt should show the size: %q", m.confirm)
	}
	if _, cmd := press(m, "n"); cmd != nil {
		t.Fatal("cancel must not delete")
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("cancel must keep the profile")
	}
	_, cmd := press(m, "y")
	if msg := cmd().(doneMsg); msg.text != "chrome data cleared" {
		t.Fatal(msg.text)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("profile should be gone")
	}
}

func TestClearRefusesNormalProfile(t *testing.T) {
	bundledProfile = defaultProfile()
	if msg := clearChrome()().(doneMsg); !strings.HasPrefix(msg.text, "refusing") {
		t.Fatalf("must refuse to delete the normal profile: %q", msg.text)
	}
}

func TestViewFitsTerminal(t *testing.T) {
	useFlags = true
	codes := strings.Fields("AE AL AR AT AU BE BG BR CA CH CL CN CO CZ DE DK EE ES FI FR GB GR HK HR HU ID IE IL IN IT JP KR LT LV MX MY NL NO NZ PL PT RO RS SE SG SK TH TR UA US VN ZA")
	for _, sz := range [][2]int{{100, 26}, {60, 18}, {36, 14}, {120, 50}, {40, 24}} {
		m := model{w: sz[0], h: sz[1], codes: codes, cursor: 22, msg: "ok", st: vpn{containerUp: true, connected: true, country: "DE", ipType: "residential"}}
		if n := strings.Count(m.View(), "\n"); n > sz[1]-1 {
			t.Errorf("%dx%d: %d lines, want at most %d", sz[0], sz[1], n, sz[1]-1)
		}
	}
}

func TestValidPort(t *testing.T) {
	for s, want := range map[string]bool{"1080": true, "1": true, "65535": true, "0": false, "65536": false, "abc": false, "": false, "-5": false} {
		if got := validPort(s); got != want {
			t.Errorf("validPort(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestComposeUsesPortVariable(t *testing.T) {
	b, _ := dockerFiles.ReadFile("docker/compose.yaml")
	if !strings.Contains(string(b), "${MYSTUI_PORT:-1080}:1080") {
		t.Fatal("compose file must map MYSTUI_PORT to port 1080 of the container")
	}
}

// fakeDocker puts a docker script on PATH that answers inspect, mystvpn status and ip link.
func fakeDocker(t *testing.T, wgLinks string) {
	if runtime.GOOS == "windows" {
		t.Skip("needs a shell script")
	}
	dir := t.TempDir()
	script := `#!/bin/sh
case "$1" in
inspect) echo true ;;
compose) if [ "$FAKE_FAIL" = compose ]; then echo boom; exit 1; fi ;;
exec)
	[ -n "$FAKE_LOG" ] && echo "$*" >> "$FAKE_LOG"
	[ -n "$FAKE_FAIL" ] && [ "$4" = "$FAKE_FAIL" ] && { echo boom; exit 1; }
	case "$3" in
	mystvpn) printf 'connected: yes\nIP address: 1.2.3.4\ncountry: AT\ncity: salzburg\n' ;;
	ip) printf '%s' "$FAKE_WG" ;;
	wg) printf '%s' "$FAKE_HS" ;;
	esac ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// the fallback for a missing `docker compose` must never reach a real docker-compose.
	if err := os.WriteFile(filepath.Join(dir, "docker-compose"), []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_WG", wgLinks)
	t.Setenv("FAKE_HS", fmt.Sprintf("KEY\t%d", time.Now().Unix()-10))
}

func TestStaleConnectionAfterRestart(t *testing.T) {
	projectDirBackup := projectDir
	projectDir = t.TempDir()
	defer func() { projectDir = projectDirBackup }()

	fakeDocker(t, "") // saved state says connected, but the container has no tunnel.
	if st := fetchStatus().(statusMsg); !st.containerUp || st.connected {
		t.Fatalf("stale state must not count as connected: %+v", st)
	}

	fakeDocker(t, "3: wg0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420\n")
	if st := fetchStatus().(statusMsg); !st.connected || st.country != "AT" {
		t.Fatalf("live tunnel must count as connected: %+v", st)
	}
}

func TestLostTunnelIsNotConnected(t *testing.T) {
	projectDirBackup := projectDir
	projectDir = t.TempDir()
	defer func() { projectDir = projectDirBackup }()

	// the interface and the saved state are there, but the exit node stopped answering.
	fakeDocker(t, "3: wg0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420\n")
	saveType("residential", "AT")
	t.Setenv("FAKE_HS", fmt.Sprintf("KEY\t%d", time.Now().Add(-6*time.Hour).Unix()))
	st := fetchStatus().(statusMsg)
	if st.connected || !strings.Contains(st.lost, "last handshake") {
		t.Fatalf("a tunnel without a recent handshake must be lost: %+v", st)
	}
	if loadType("AT") != "residential" {
		t.Fatal("a lost tunnel must keep the IP type for the reconnect")
	}
	if view := (model{st: vpn(st), w: 100, h: 30}).View(); !strings.Contains(view, "connection lost") {
		t.Fatalf("the view must say the connection is lost:\n%s", view)
	}
}

func TestLostTunnelReconnectsOncePerInterval(t *testing.T) {
	lost := statusMsg{containerUp: true, lost: "last handshake 6h0m0s ago"}
	next, cmd := model{}.Update(lost)
	m := next.(model)
	if cmd == nil || !strings.HasPrefix(m.busy, "reconnecting") {
		t.Fatalf("a lost tunnel must start a reconnect: busy=%q", m.busy)
	}
	m.busy = ""
	if _, cmd := m.Update(lost); cmd != nil {
		t.Fatal("the next status inside the interval must not reconnect again")
	}
	m.repaired = time.Now().Add(-2 * repairInterval)
	if _, cmd := m.Update(lost); cmd == nil {
		t.Fatal("a lost tunnel must reconnect again after the interval")
	}
}

func TestReconnectRemovesTheDeadInterfaceFirst(t *testing.T) {
	projectDirBackup := projectDir
	projectDir = t.TempDir()
	defer func() { projectDir = projectDirBackup }()

	fakeDocker(t, "")
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_LOG", log)
	// every request of mystvpn enters a dead tunnel, so its interface has to go before anything else.
	msg := reconnect("AT", "", "mvpn-1")().(doneMsg)
	if !strings.HasPrefix(msg.text, "reconnected") {
		t.Fatalf("reconnect failed: %s", msg.text)
	}
	calls, _ := os.ReadFile(log)
	del := strings.Index(string(calls), "ip link delete mvpn-1")
	disconnect := strings.Index(string(calls), "mystvpn disconnect")
	connect := strings.Index(string(calls), "mystvpn connect --country AT --ip-type residential")
	if del < 0 || disconnect < del || connect < disconnect {
		t.Fatalf("the order must be delete, disconnect, connect:\n%s", calls)
	}
	if loadType("AT") != "residential" {
		t.Fatal("the reconnect must remember the IP type")
	}
}
