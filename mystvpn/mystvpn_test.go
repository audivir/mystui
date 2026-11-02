package mystvpn

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var epoch = time.Unix(1_800_000_000, 0)

func TestHandshakeAgeUsesNewestPeer(t *testing.T) {
	out := "KEY1\t1799999000\nKEY2\t1799999990\nKEY3\t0"
	age, ok := HandshakeAge(out, epoch)
	if !ok || age != 10*time.Second {
		t.Fatal(age, ok)
	}
	if _, ok := HandshakeAge("KEY\t0", epoch); ok {
		t.Fatal("a peer that never shook hands has no age")
	}
}

func TestTunnelInterfaceSkipsExcluded(t *testing.T) {
	links := "1: wg0: <POINTOPOINT>\n3: mvpn-9@NONE: <POINTOPOINT>"
	if got := TunnelInterface(links, "wg0"); got != "mvpn-9" {
		t.Fatal(got)
	}
	if got := TunnelInterface(links); got != "wg0" {
		t.Fatal(got)
	}
	if got := TunnelInterface("1: wg0: <UP>", "wg0"); got != "" {
		t.Fatal(got)
	}
}

func TestTunnelFromContainerOutput(t *testing.T) {
	answers := map[string]string{
		"ip -o link":      "1: wg0: <UP>\n2: mvpn-1: <UP>",
		"wg show mvpn-1":  "KEY\t1799990000",
		"mystvpn connect": "connected",
		"mystvpn status":  "connected: yes\nIP address: 1.2.3.4\ncountry: AT\ncity: wien",
	}
	c := &Client{Container: "box", Run: func(name string, args ...string) (string, error) {
		call := name + " " + strings.Join(args, " ")
		for k, v := range answers {
			if strings.Contains(call, k) {
				return v, nil
			}
		}
		return "", errors.New("unexpected " + call)
	}}
	tun := c.Tunnel(epoch, "wg0")
	if tun.Interface != "mvpn-1" || tun.Up() || !strings.Contains(tun.Describe(), "last handshake") {
		t.Fatalf("a handshake of 10000 seconds ago must not count as up: %+v", tun)
	}
	answers["wg show mvpn-1"] = "KEY\t1799999990"
	if !c.Tunnel(epoch, "wg0").Up() {
		t.Fatal("a fresh handshake is up")
	}
	st, _, err := c.Status()
	if err != nil || !st.Connected || st.IP != "1.2.3.4" || st.Country != "AT" || st.City != "wien" {
		t.Fatal(st, err)
	}
}

func TestReconnectRemovesTheDeadInterfaceFirst(t *testing.T) {
	var calls []string
	c := &Client{Container: "box", Run: func(name string, args ...string) (string, error) {
		call := strings.Join(args, " ")
		calls = append(calls, call)
		if strings.Contains(call, "link delete") || strings.Contains(call, "disconnect") {
			return "not up", errors.New("exit 1")
		}
		return "connected", nil
	}}
	out, err := c.Reconnect("AT", "residential", "mvpn-1")
	if err != nil || out != "connected" {
		t.Fatal(out, err)
	}
	want := []string{
		"exec box ip link delete mvpn-1",
		"exec box mystvpn disconnect",
		"exec box mystvpn connect --country AT --ip-type residential",
	}
	if strings.Join(calls, "|") != strings.Join(want, "|") {
		t.Fatalf("failing teardown steps must not stop the connect, and the order matters: %v", calls)
	}
}

// watchdogEnv writes the scripts and fake ip, wg, and mystvpn.real commands that log their calls.
type watchdogEnv struct {
	t         *testing.T
	dir       string
	handshake int64
	// nudge is the command that stands for the packet that starts a handshake.
	nudge string
}

func newWatchdogEnv(t *testing.T) *watchdogEnv {
	dir := t.TempDir()
	if err := WriteScripts(dir); err != nil {
		t.Fatal(err)
	}
	fakes := map[string]string{
		"ip": `#!/bin/sh
echo "ip $*" >> "$DATA_DIR/calls"
case "$*" in "-o link show type wireguard") printf '2: wg0: <UP>\n3: mvpn-1: <UP>\n' ;; esac`,
		"wg":           "#!/bin/sh\nprintf 'KEY\\t%s\\n' \"$(cat \"$DATA_DIR/handshake\")\"",
		"mystvpn.real": "#!/bin/sh\necho \"real $*\" >> \"$DATA_DIR/calls\"\n[ \"$FAKE_FAIL\" = \"$1\" ] && exit 1\nexit 0",
	}
	for name, body := range fakes {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return &watchdogEnv{t: t, dir: dir, handshake: time.Now().Unix() - 10, nudge: "true"}
}

func (w *watchdogEnv) run(script string, args ...string) string {
	out, err := w.try(script, args...)
	if err != nil {
		w.t.Fatalf("%s failed: %v\n%s", script, err, out)
	}
	return out
}

func (w *watchdogEnv) try(script string, args ...string) (string, error) {
	if err := os.WriteFile(filepath.Join(w.dir, "handshake"), []byte(fmt.Sprint(w.handshake)), 0o600); err != nil {
		w.t.Fatal(err)
	}
	cmd := exec.Command("sh", append([]string{filepath.Join(w.dir, script)}, args...)...)
	cmd.Env = []string{
		"PATH=" + w.dir + ":/usr/bin:/bin",
		"DATA_DIR=" + w.dir,
		"MYSTVPN_REAL=" + filepath.Join(w.dir, "mystvpn.real"),
		"MYSTVPN=" + filepath.Join(w.dir, "mystvpn-wrapper.sh"),
		"WATCHDOG_ONCE=1",
		"WATCHDOG_NUDGE_WAIT=0",
		"WATCHDOG_NUDGE=" + w.nudge,
		"FAKE_FAIL=" + os.Getenv("FAKE_FAIL"),
	}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (w *watchdogEnv) calls() string {
	b, _ := os.ReadFile(filepath.Join(w.dir, "calls"))
	return strings.TrimSpace(string(b))
}

func (w *watchdogEnv) connectedAt(age int64) {
	line := fmt.Sprintf("%d connect --country AT --ip-type residential\n", time.Now().Unix()-age)
	if err := os.WriteFile(filepath.Join(w.dir, ".last-connect"), []byte(line), 0o600); err != nil {
		w.t.Fatal(err)
	}
}

func TestWatchdogReplacesADeadTunnel(t *testing.T) {
	w := newWatchdogEnv(t)
	w.connectedAt(3600)
	w.handshake = time.Now().Unix() - 6*3600
	out := w.run("watchdog.sh")
	want := []string{
		"ip -o link show type wireguard",
		"ip link delete mvpn-1",
		"real disconnect",
		"real connect --country AT --ip-type residential",
	}
	if w.calls() != strings.Join(want, "\n") {
		t.Fatalf("the dead interface must go before disconnect and connect:\n%s", w.calls())
	}
	if !strings.Contains(out, "connected again") {
		t.Fatal(out)
	}
}

func TestWatchdogKeepsTheTunnelWhenTheHandshakeReturnsAfterANudge(t *testing.T) {
	w := newWatchdogEnv(t)
	w.connectedAt(3600)
	w.handshake = time.Now().Unix() - 6*3600
	// the exit node still knows the tunnel, so the first packet starts a handshake.
	w.nudge = "date +%s > \"" + filepath.Join(w.dir, "handshake") + "\""
	out := w.run("watchdog.sh")
	if !strings.Contains(out, "recovered by itself") || strings.Contains(w.calls(), "delete") || strings.Contains(w.calls(), "real connect") {
		t.Fatalf("a tunnel that shakes hands again must keep its exit address:\n%s\n%s", out, w.calls())
	}
}

func TestWatchdogLeavesHealthyAndFreshAndDisconnectedTunnelsAlone(t *testing.T) {
	cases := map[string]func(w *watchdogEnv){
		"healthy":      func(w *watchdogEnv) { w.connectedAt(3600) },
		"just started": func(w *watchdogEnv) { w.connectedAt(5); w.handshake = 0 },
		"taken down":   func(w *watchdogEnv) { w.handshake = 0 },
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWatchdogEnv(t)
			setup(w)
			w.run("watchdog.sh")
			if strings.Contains(w.calls(), "delete") || strings.Contains(w.calls(), "real connect") {
				t.Fatalf("the tunnel must be left alone:\n%s", w.calls())
			}
		})
	}
}

func TestWatchdogRepairsAMissingInterfaceAfterARestart(t *testing.T) {
	w := newWatchdogEnv(t)
	w.connectedAt(3600)
	if err := os.WriteFile(filepath.Join(w.dir, "ip"), []byte("#!/bin/sh\necho \"ip $*\" >> \"$DATA_DIR/calls\""), 0o755); err != nil {
		t.Fatal(err)
	}
	w.run("watchdog.sh")
	if !strings.Contains(w.calls(), "real connect --country AT") || strings.Contains(w.calls(), "link delete") {
		t.Fatalf("a missing interface needs a connect and nothing to delete:\n%s", w.calls())
	}
}

func TestWrapperRecordsOnlyWhatTheWatchdogMayRepeat(t *testing.T) {
	w := newWatchdogEnv(t)
	last := filepath.Join(w.dir, ".last-connect")
	t.Setenv("FAKE_FAIL", "connect")
	if _, err := w.try("mystvpn-wrapper.sh", "connect", "--country", "AT", "--ip-type", "residential"); err == nil {
		t.Fatal("the wrapper must pass the exit status on")
	}
	if _, err := os.Stat(last); err == nil {
		t.Fatal("a failed connect must not be recorded")
	}
	t.Setenv("FAKE_FAIL", "")
	w.run("mystvpn-wrapper.sh", "connect", "--country", "DE", "--ip-type", "hosting")
	if b, _ := os.ReadFile(last); !strings.HasSuffix(strings.TrimSpace(string(b)), "connect --country DE --ip-type hosting") {
		t.Fatalf("the connect is not recorded: %q", b)
	}
	t.Setenv("FAKE_FAIL", "disconnect")
	if _, err := w.try("mystvpn-wrapper.sh", "disconnect"); err == nil {
		t.Fatal("the wrapper must pass the exit status on")
	}
	if _, err := os.Stat(last); err == nil {
		t.Fatal("a disconnect must stop the repair even if it reports an error")
	}
}

func TestKillswitchAllowsTheProxyUserOnlyThroughTheTunnel(t *testing.T) {
	dir := t.TempDir()
	if err := WriteScripts(dir); err != nil {
		t.Fatal(err)
	}
	// -C reports a missing rule, so every rule gets added.
	fake := "#!/bin/sh\necho \"$(basename \"$0\") $*\" >> \"$DATA_DIR/calls\"\n[ \"$1\" = -C ] && exit 1\nexit 0\n"
	for _, name := range []string{"iptables", "ip6tables"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(fake), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := func(env ...string) string {
		_ = os.Remove(filepath.Join(dir, "calls"))
		cmd := exec.Command("sh", filepath.Join(dir, "killswitch.sh"))
		cmd.Env = append([]string{"PATH=" + dir + ":/usr/bin:/bin", "DATA_DIR=" + dir}, env...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("killswitch failed: %v\n%s", err, out)
		}
		b, _ := os.ReadFile(filepath.Join(dir, "calls"))
		return string(b)
	}

	got := run()
	want := []string{
		"iptables -N KILLSWITCH",
		"iptables -A KILLSWITCH -o lo -j ACCEPT",
		"iptables -A KILLSWITCH -o mvpn+ -j ACCEPT",
		"iptables -A KILLSWITCH -j REJECT",
		"iptables -C OUTPUT -m owner --uid-owner proxy -j KILLSWITCH",
		"iptables -I OUTPUT 1 -m owner --uid-owner proxy -j KILLSWITCH",
	}
	for _, line := range want {
		if !strings.Contains(got, line+"\n") {
			t.Fatalf("missing %q in:\n%s", line, got)
		}
	}
	// the reject comes after every accept, or the allowed interfaces would be cut off too.
	if strings.Index(got, "-j REJECT") < strings.LastIndex(got[:strings.Index(got, "ip6tables")], "-j ACCEPT") {
		t.Fatalf("REJECT must be the last rule of the chain:\n%s", got)
	}
	if !strings.Contains(got, "ip6tables -I OUTPUT 1") {
		t.Fatalf("IPv6 needs the same rules, or it would bypass the kill-switch:\n%s", got)
	}

	got = run("KILLSWITCH_SPORT=1080", "KILLSWITCH_FORWARD_IN=wg0", "KILLSWITCH_ALLOW=lo mvpn+ wg0")
	for _, line := range []string{
		"iptables -A KILLSWITCH -o wg0 -j ACCEPT",
		"iptables -A KILLSWITCH -p tcp --sport 1080 -j ACCEPT",
		"iptables -I FORWARD 1 -i wg0 ! -o mvpn+ ! -o wg0 -j REJECT",
	} {
		if !strings.Contains(got, line+"\n") {
			t.Fatalf("missing %q in:\n%s", line, got)
		}
	}
}
