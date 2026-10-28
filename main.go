// Command mystui is a terminal front end for the Mysterium VPN CLI running in Docker.
package main

import (
	"context"
	"embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/text/language"
	"golang.org/x/text/language/display"
)

// version is set with -ldflags -X main.version=... for release builds, resolved from the
// module build info for go install, and falls back to dev otherwise.
var version = "dev"

func init() {
	if version != "dev" {
		return
	}
	if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
		version = info.Main.Version
	}
}

const container = "mystvpn"

var (
	home           = userHome()
	projectDir     = envOr("MYSTUI_DIR", filepath.Join(xdgDir("XDG_DATA_HOME", ".local/share"), "mystui"))
	socksPort      = envOr("MYSTUI_PORT", "1080") // host port, the container always uses 1080.
	socksAddr      = "127.0.0.1:" + socksPort
	composeFile    = filepath.Join(projectDir, "docker", "compose.yaml")
	bundledProfile = filepath.Join(projectDir, "chrome")
	chromeProfile  = profilePath()

	bold = lipgloss.NewStyle().Bold(true)
	dim  = lipgloss.NewStyle().Faint(true)
	good = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	bad  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	warn = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	sel  = lipgloss.NewStyle().Reverse(true)

	ipTypes   = []string{"residential", "hosting"}
	codeRe    = regexp.MustCompile(`^[A-Z]{2}$`)
	errAuthRe = regexp.MustCompile(`authentication required|not signed in|auth`)
)

// defaultProfile returns the data directory of the normal Chrome or Chromium on this OS.
func defaultProfile() string {
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Google", "Chrome")
	case "windows":
		return filepath.Join(os.Getenv("LocalAppData"), "Google", "Chrome", "User Data")
	}
	if _, err := exec.LookPath("google-chrome"); err != nil {
		if _, err := exec.LookPath("google-chrome-stable"); err != nil {
			if _, err := exec.LookPath("chromium"); err == nil {
				return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "chromium")
			}
		}
	}
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), "google-chrome")
}

// profilePath returns the profile that the "c" key opens.
// MYSTUI_CHROME_PROFILE overrides the normal Chrome profile.
func profilePath() string {
	if p := os.Getenv("MYSTUI_CHROME_PROFILE"); p != "" {
		return p
	}
	return defaultProfile()
}

// validPort reports whether s is a usable TCP port number.
func validPort(s string) bool {
	n, err := strconv.Atoi(s)
	return err == nil && n >= 1 && n <= 65535
}

// xdgDir returns the base directory named by an XDG variable, or fallback below the home directory.
// The specification requires the variable to hold an absolute path, so other values are ignored.
func xdgDir(name, fallback string) string {
	if p := os.Getenv(name); filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(home, filepath.FromSlash(fallback))
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

//go:embed docker/Dockerfile docker/compose.yaml
var dockerFiles embed.FS

// writeFiles writes the embedded Dockerfile and compose file into the docker subdirectory of the
// project directory and creates the private data directory that the container mounts.
// The build context is the docker subdirectory, so tokens and keys never reach the daemon.
// The files are rewritten on every call, so they always match this binary.
func writeFiles() error {
	if err := os.MkdirAll(filepath.Join(projectDir, "data"), 0o700); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(projectDir, "docker"), 0o755); err != nil {
		return err
	}
	for _, name := range []string{"Dockerfile", "compose.yaml"} {
		data, err := dockerFiles.ReadFile("docker/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(projectDir, "docker", name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func userHome() string {
	h, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return h
}

// run executes mystvpn inside the container and returns its combined output.
func run(timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", append([]string{"exec", container, "mystvpn"}, args...)...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// vpn stores the state of the container and the VPN connection.
type vpn struct {
	containerUp bool
	socksUp     bool
	connected   bool
	ip, country string
	city        string
	ipType      string // residential or hosting, empty when unknown.
}

// typeFile returns the path of the file that remembers the IP type of the connection.
// The status command does not report the IP type, so mystui stores it together with the country.
// The country keeps a connection made elsewhere from being mislabeled.
func typeFile() string { return filepath.Join(projectDir, "iptype") }

func saveType(ipType, country string) {
	if ipType == "" {
		_ = os.Remove(typeFile())
		return
	}
	_ = os.WriteFile(typeFile(), []byte(ipType+" "+country), 0o600)
}

func loadType(country string) string {
	f := strings.Fields(func() string { b, _ := os.ReadFile(typeFile()); return string(b) }())
	if len(f) == 2 && f[1] == country {
		return f[0]
	}
	return ""
}

// model stores the state of the interface and implements the Bubble Tea model.
type model struct {
	st       vpn
	codes    []string
	needAuth bool
	ipType   int
	cursor   int
	busy     string
	help     bool   // show the full key list even on a short terminal.
	confirm  string // text of the pending yes/no prompt, empty when none.
	prompt   string // which prompt is pending, "clear" or "separate".
	msg      string
	w, h     int
	listErr  string
}

type (
	statusMsg vpn
	codesMsg  struct {
		codes    []string
		needAuth bool
		err      string
	}
	doneMsg struct{ text string }
	tickMsg time.Time
)

func fetchStatus() tea.Msg {
	var s vpn
	if c, err := net.DialTimeout("tcp", socksAddr, time.Second); err == nil {
		_ = c.Close()
		s.socksUp = true
	}
	// docker exec prints an error for a missing container, so ask Docker whether it runs.
	if out, err := exec.Command("docker", "inspect", "-f", "{{.State.Running}}", container).Output(); err != nil || strings.TrimSpace(string(out)) != "true" {
		return statusMsg(s)
	}
	s.containerUp = true
	out, _ := run(5*time.Second, "status")
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "connected":
			s.connected = v == "yes"
		case "IP address":
			s.ip = v
		case "country":
			s.country = v
		case "city":
			s.city = v
		}
	}
	// status reads saved state, which outlives a container restart, but the WireGuard
	// interface does not, so a connection without one is stale.
	if s.connected {
		args := []string{"exec", container, "ip", "-o", "link", "show", "type", "wireguard"}
		if link, _ := exec.Command("docker", args...).Output(); strings.TrimSpace(string(link)) == "" {
			s.connected = false
		}
	}
	if s.connected {
		s.ipType = loadType(s.country)
	} else {
		saveType("", "")
	}
	return statusMsg(s)
}

func fetchCodes(ipType string) tea.Cmd {
	return func() tea.Msg {
		out, err := run(30*time.Second, "countries", "--ip-type", ipType)
		if err != nil {
			return codesMsg{needAuth: errAuthRe.MatchString(out), err: lastLine(out)}
		}
		var codes []string
		for _, f := range strings.Fields(out) {
			if codeRe.MatchString(f) {
				codes = append(codes, f)
			}
		}
		sort.SliceStable(codes, func(i, j int) bool { return countryName(codes[i]) < countryName(codes[j]) })
		return codesMsg{codes: codes}
	}
}

func connect(code, ipType string) tea.Cmd {
	return func() tea.Msg {
		out, err := run(90*time.Second, "connect", "--country", code, "--ip-type", ipType)
		if err != nil {
			return doneMsg{"connect failed: " + lastLine(out)}
		}
		saveType(ipType, code)
		return doneMsg{"connected: " + strings.ReplaceAll(out, "\n", "  ")}
	}
}

func simple(label string, args ...string) tea.Cmd {
	return func() tea.Msg {
		if out, err := run(60*time.Second, args...); err != nil {
			return doneMsg{label + " failed: " + lastLine(out)}
		}
		return doneMsg{label + " done"}
	}
}

func compose(args ...string) tea.Cmd {
	return func() tea.Msg {
		if err := writeFiles(); err != nil {
			return doneMsg{"cannot write docker files: " + err.Error()}
		}
		name, pre := "docker", []string{"compose"}
		if exec.Command("docker", "compose", "version").Run() != nil {
			name, pre = "docker-compose", nil
		}
		full := append(append(pre, "-f", composeFile), args...)
		if out, err := exec.Command(name, full...).CombinedOutput(); err != nil {
			return doneMsg{"compose failed: " + lastLine(string(out))}
		}
		return doneMsg{"compose " + args[0] + " done"}
	}
}

// login hands the terminal to `mystvpn auth`, which prints the approval URL.
func login() tea.Cmd {
	c := exec.Command("docker", "exec", "-it", container, "mystvpn", "auth")
	return tea.ExecProcess(c, func(err error) tea.Msg {
		if err != nil {
			return doneMsg{"sign-in failed: " + err.Error()}
		}
		return doneMsg{"signed in"}
	})
}

// chromeCmd builds the command that starts Chrome (or Chromium) on this OS.
func chromeCmd(args []string) (*exec.Cmd, error) {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", append([]string{"-na", "Google Chrome", "--args"}, args...)...), nil
	case "windows":
		for _, env := range []string{"ProgramFiles", "ProgramFiles(x86)", "LocalAppData"} {
			if dir := os.Getenv(env); dir != "" {
				p := filepath.Join(dir, "Google", "Chrome", "Application", "chrome.exe")
				if _, err := os.Stat(p); err == nil {
					return exec.Command(p, args...), nil
				}
			}
		}
		return nil, fmt.Errorf("chrome.exe not found")
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return exec.Command(p, args...), nil
		}
	}
	return nil, fmt.Errorf("no chrome or chromium found in PATH")
}

func sameDir(a, b string) bool { return filepath.Clean(a) == filepath.Clean(b) }

// userDataDir extracts the value of --user-data-dir from a command line, spaces included.
func userDataDir(line string) string {
	_, rest, ok := strings.Cut(line, "--user-data-dir=")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, " --"); i >= 0 {
		rest = rest[:i]
	}
	return strings.TrimSpace(rest)
}

// isChromeMain reports whether a command line belongs to a Chrome browser, not a helper.
func isChromeMain(line string) bool {
	if strings.Contains(line, " --type=") || strings.Contains(line, "Helper") {
		return false
	}
	if strings.Contains(line, "/Google Chrome.app/Contents/MacOS/Google Chrome") {
		return true
	}
	f := strings.Fields(line)
	if len(f) == 0 {
		return false
	}
	switch filepath.Base(f[0]) {
	case "google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome":
		return true
	}
	return false
}

// chromeRunning reports whether Chrome is running on the given profile.
// A browser without --user-data-dir uses the normal profile.
var chromeRunningFn = chromeRunning

func chromeRunning(profile string) bool {
	normal := sameDir(profile, defaultProfile())
	// command lines are not readily available on Windows, so only the normal profile is checked.
	if runtime.GOOS == "windows" {
		out, _ := exec.Command("tasklist", "/FI", "IMAGENAME eq chrome.exe", "/NH").Output()
		return normal && strings.Contains(strings.ToLower(string(out)), "chrome.exe")
	}
	out, err := exec.Command("ps", "-axo", "args=").Output()
	if err != nil {
		return false
	}
	return runningIn(strings.Split(string(out), "\n"), profile, normal)
}

// runningIn reports whether one of the command lines is a Chrome browser on the profile.
func runningIn(lines []string, profile string, normal bool) bool {
	for _, line := range lines {
		if !isChromeMain(line) {
			continue
		}
		if dir := userDataDir(line); dir == "" && normal || dir != "" && sameDir(dir, profile) {
			return true
		}
	}
	return false
}

// chrome returns a command that starts Chrome through the proxy on the given profile.
func chrome(profile string) tea.Cmd {
	return func() tea.Msg {
		args := []string{"--proxy-server=socks5://" + socksAddr, "--proxy-bypass-list=<-loopback>"}
		label := "normal profile"
		if !sameDir(profile, defaultProfile()) {
			args = append([]string{"--user-data-dir=" + profile}, args...)
			label = filepath.Base(profile)
		}
		cmd, err := chromeCmd(args)
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			return doneMsg{"chrome failed: " + err.Error()}
		}
		go func() { _ = cmd.Wait() }() // reap the process while the browser keeps running.
		return doneMsg{"chrome started through the proxy (" + label + ")"}
	}
}

type keyItem struct{ key, label string }

var keyStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6"))

// renderKeys lays out the key hints as "key label" pairs, wrapped to width.
func renderKeys(items []keyItem, width int) string {
	var lines []string
	line, plain := "", 0
	for _, it := range items {
		w := len([]rune(it.key)) + 1 + len([]rune(it.label))
		if line != "" && plain+3+w > width {
			lines = append(lines, line)
			line, plain = "", 0
		}
		if line != "" {
			line += "   "
			plain += 3
		}
		line += keyStyle.Render(it.key) + " " + dim.Render(it.label)
		plain += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

var keyHelp = []keyItem{
	{"enter", "connect"}, {"d", "disconnect"}, {"f", "refresh"}, {"t", "ip type"},
	{"a", "sign in"}, {"L", "sign out"}, {"u", "start container"}, {"x", "stop container"},
	{"c", "chrome via proxy"}, {"C", "clear separate chrome data"}, {"r", "reload"}, {"?", "short help"}, {"q", "quit"},
}

var shortKeys = []keyItem{{"enter", "connect"}, {"d", "disconnect"}, {"?", "all keys"}, {"q", "quit"}}

// clearChrome returns a command that deletes the bundled profile, never the normal or a custom one.
func clearChrome() tea.Cmd {
	return func() tea.Msg {
		if filepath.Base(bundledProfile) != "chrome" || sameDir(bundledProfile, defaultProfile()) {
			return doneMsg{"refusing to delete " + bundledProfile}
		}
		if err := os.RemoveAll(bundledProfile); err != nil {
			return doneMsg{"clearing chrome data failed: " + err.Error()}
		}
		return doneMsg{"chrome data cleared"}
	}
}

func dirSize(dir string) (int64, error) {
	if _, err := os.Stat(dir); err != nil {
		return 0, err
	}
	var total int64
	_ = filepath.WalkDir(dir, func(_ string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, e := d.Info(); e == nil {
				total += info.Size()
			}
		}
		return nil
	})
	return total, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n/1024)
}

func tick() tea.Cmd {
	return tea.Tick(5*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m model) Init() tea.Cmd {
	return tea.Batch(func() tea.Msg { return fetchStatus() }, fetchCodes(ipTypes[0]), tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.w, m.h = msg.Width, msg.Height
	case statusMsg:
		m.st = vpn(msg)
	case tickMsg:
		return m, tea.Batch(func() tea.Msg { return fetchStatus() }, tick())
	case codesMsg:
		m.codes, m.needAuth, m.listErr = msg.codes, msg.needAuth, msg.err
		if m.cursor >= len(m.codes) {
			m.cursor = 0
		}
	case doneMsg:
		m.busy, m.msg = "", msg.text
		return m, tea.Batch(func() tea.Msg { return fetchStatus() }, fetchCodes(ipTypes[m.ipType]))
	case tea.KeyMsg:
		return m.key(msg)
	}
	return m, nil
}

// useFlags is true when flag emoji are drawn. MYSTUI_FLAGS=1 or 0 forces the choice, otherwise
// a UTF-8 locale turns them on.
var useFlags = func() bool {
	switch os.Getenv("MYSTUI_FLAGS") {
	case "0":
		return false
	case "1":
		return true
	}
	if os.Getenv("TERM") == "linux" {
		return false
	}
	for _, v := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if s := strings.ToLower(os.Getenv(v)); s != "" {
			return strings.Contains(s, "utf-8") || strings.Contains(s, "utf8")
		}
	}
	return false
}()

// flag returns the flag emoji of a two-letter country code, or an empty string when flags are off.
func flag(code string) string {
	if !useFlags || len(code) != 2 {
		return ""
	}
	return string([]rune{0x1F1E6 + rune(code[0]-'A'), 0x1F1E6 + rune(code[1]-'A')})
}

// countryName returns the English name for a two-letter region code.
func countryName(code string) string {
	r, err := language.ParseRegion(code)
	if err != nil {
		return code
	}
	if n := display.English.Regions().Name(r); n != "" {
		return n
	}
	return code
}

func trunc(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func pad(s string, n int) string {
	if k := n - len([]rune(s)); k > 0 {
		return s + strings.Repeat(" ", k)
	}
	return s
}

const maxName = 24

// cellGeometry returns the width of the name column and of a whole grid cell.
// A cell holds marker, flag or code (2 columns), space, name, and gap.
func cellGeometry(codes []string) (nameW, cellW int) {
	for _, c := range codes {
		if l := len([]rune(trunc(countryName(c), maxName))); l > nameW {
			nameW = l
		}
	}
	return nameW, 1 + 2 + 1 + nameW + 2
}

func (m model) cols() int {
	_, cw := cellGeometry(m.codes)
	c := (m.w - 1) / cw
	if c < 1 {
		c = 1
	}
	return c
}

func (m model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	n := len(m.codes)
	if m.confirm != "" { // a pending prompt takes all key presses.
		prompt := m.prompt
		m.confirm, m.prompt = "", ""
		if k.String() == "y" || k.String() == "Y" {
			switch prompt {
			case "clear":
				m.busy = "clearing chrome data"
				return m, clearChrome()
			case "separate":
				return m, chrome(bundledProfile)
			}
		}
		m.msg = "cancelled"
		return m, nil
	}
	switch k.String() {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "left", "h":
		if m.cursor > 0 {
			m.cursor--
		}
	case "right", "l":
		if m.cursor < n-1 {
			m.cursor++
		}
	case "up", "k":
		if m.cursor-m.cols() >= 0 {
			m.cursor -= m.cols()
		}
	case "down", "j":
		if m.cursor+m.cols() < n {
			m.cursor += m.cols()
		}
	case "t":
		m.ipType = (m.ipType + 1) % len(ipTypes)
		m.codes, m.cursor = nil, 0
		return m, fetchCodes(ipTypes[m.ipType])
	case "C":
		size, err := dirSize(bundledProfile)
		if err != nil {
			m.msg = "no separate chrome data yet"
			break
		}
		m.prompt, m.confirm = "clear", fmt.Sprintf("%s (%s)", bundledProfile, humanSize(size))
	case "?":
		m.help = !m.help
	case "r":
		m.msg = "refreshing"
		return m, tea.Batch(func() tea.Msg { return fetchStatus() }, fetchCodes(ipTypes[m.ipType]))
	case "enter":
		if m.busy != "" || n == 0 {
			break
		}
		m.busy = "connecting to " + m.codes[m.cursor]
		return m, connect(m.codes[m.cursor], ipTypes[m.ipType])
	case "d":
		m.busy = "disconnecting"
		return m, simple("disconnect", "disconnect")
	case "f":
		m.busy = "reconnecting"
		return m, simple("refresh", "refresh")
	case "a":
		return m, login()
	case "L":
		m.busy = "signing out"
		return m, simple("sign out", "logout")
	case "u":
		m.busy = "starting container"
		return m, compose("up", "-d", "--build")
	case "x":
		m.busy = "stopping container"
		return m, compose("down")
	case "c":
		if chromeRunningFn(chromeProfile) { // a running Chrome ignores the proxy flags.
			m.prompt, m.confirm = "separate", bundledProfile
			return m, nil
		}
		return m, chrome(chromeProfile)
	}
	return m, nil
}

func (m model) View() string {
	var b strings.Builder
	st := m.st
	width := m.w
	if width <= 0 {
		width = 80
	}
	footer := renderKeys(keyHelp, width)
	if !m.help && m.h > 0 && (strings.Count(footer, "\n")+1)*6 > m.h {
		footer = renderKeys(shortKeys, width)
	}
	switch m.prompt {
	case "clear":
		footer = warn.Render("Delete the separate Chrome data used by the proxy?") + "\n" + m.confirm + "\n" +
			dim.Render("Logins, cookies, history and extensions in that profile are lost. Your normal Chrome profile is not touched.") + "\n" +
			keyStyle.Render("y") + " " + dim.Render("delete") + "   " + keyStyle.Render("any other key") + " " + dim.Render("cancel")
	case "separate":
		footer = warn.Render("Chrome is already running on "+chromeProfile+".") + "\n" +
			dim.Render("A new window would reuse it and ignore the proxy. Open a separate proxied profile instead?") + "\n" +
			m.confirm + dim.Render("  (empty on first use, kept for next time)") + "\n" +
			keyStyle.Render("y") + " " + dim.Render("open separate profile") + "   " + keyStyle.Render("any other key") + " " + dim.Render("cancel")
	}
	b.WriteString(bold.Render("Mysterium VPN") + "\n\n")

	cont := bad.Render("down")
	if st.containerUp {
		cont = good.Render("up")
	}
	socks := bad.Render("closed")
	if st.socksUp {
		socks = good.Render("open")
	}
	fmt.Fprintf(&b, "container %s   socks5 %s %s\n", cont, socks, dim.Render(socksAddr))
	if st.connected {
		kind := ""
		if st.ipType != "" {
			kind = " " + dim.Render("("+st.ipType+")")
		}
		fmt.Fprintf(&b, "vpn %s%s   exit %s  %s %s\n", good.Render("connected"), kind, st.ip, st.city, strings.TrimSpace(flag(st.country)+" "+st.country))
	} else {
		fmt.Fprintf(&b, "vpn %s\n", warn.Render("not connected"))
	}

	b.WriteString(dim.Render(strings.Repeat("─", width)) + "\n")
	fmt.Fprintf(&b, "%s  %s  %s\n", bold.Render("Countries"), dim.Render(ipTypes[m.ipType]+" IPs"), dim.Render(fmt.Sprintf("%d available", len(m.codes))))
	switch {
	case !st.containerUp:
		b.WriteString(warn.Render("container is down: press u to start it") + "\n")
	case m.needAuth:
		b.WriteString(warn.Render("not signed in: press a, open the printed URL, approve, then come back") + "\n")
	case m.listErr != "":
		b.WriteString(bad.Render(m.listErr) + "\n")
	}
	// reserve the footer lines plus the blank line above them.
	m.writeGrid(&b, width, strings.Count(footer, "\n")+2)
	if m.busy != "" {
		b.WriteString(warn.Render(m.busy+"...") + "\n")
	} else if m.msg != "" {
		b.WriteString(m.msg + "\n")
	}
	b.WriteString("\n" + footer + "\n")
	return b.String()
}

// writeGrid draws the country grid, scrolled so the cursor row stays visible.
func (m model) writeGrid(b *strings.Builder, width, reserve int) {
	n := len(m.codes)
	if n == 0 {
		return
	}
	nameW, _ := cellGeometry(m.codes)
	cols := m.cols()
	rows := (n + cols - 1) / cols

	visible := rows
	if m.h > 0 {
		// leave room for the scroll hint, message, spare line, and footer.
		visible = m.h - strings.Count(b.String(), "\n") - 3 - reserve
		if visible < 1 {
			visible = 1
		}
	}
	top := 0
	if visible < rows {
		top = m.cursor/cols - visible/2
		if top < 0 {
			top = 0
		}
		if top > rows-visible {
			top = rows - visible
		}
	} else {
		visible = rows
	}

	for r := top; r < top+visible; r++ {
		for c := 0; c < cols; c++ {
			i := r*cols + c
			if i >= n {
				break
			}
			code := m.codes[i]
			mark, left := " ", code
			if f := flag(code); f != "" {
				left = f
			}
			isCurrent := m.st.connected && code == m.st.country && m.st.ipType == ipTypes[m.ipType]
			if isCurrent {
				mark = "●"
			}
			cell := mark + left + " " + pad(trunc(countryName(code), maxName), nameW) + "  "
			switch {
			case i == m.cursor:
				cell = sel.Render(cell)
			case isCurrent:
				cell = good.Render(cell)
			}
			b.WriteString(cell)
		}
		b.WriteString("\n")
	}
	if visible >= rows {
		b.WriteString("\n")
	} else {
		fmt.Fprintf(b, "%s\n", dim.Render(fmt.Sprintf("↑ %d rows above · ↓ %d below · ● connected", top, rows-top-visible)))
	}
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

const usage = `mystui: terminal front end for the Mysterium VPN CLI, running in Docker

usage:
  mystui          start the interface
  mystui version  print the version
  mystui help     show this text

environment:
  MYSTUI_DIR              directory for generated files, data, and Chrome profile (default $XDG_DATA_HOME/mystui or ~/.local/share/mystui)
  MYSTUI_PORT             host port of the SOCKS5 proxy (default 1080)
  MYSTUI_CHROME_PROFILE   Chrome profile to open through the proxy (default: your normal profile)
  MYSTUI_FLAGS            1 or 0 forces flag emoji on or off (default: on for UTF-8 locales)
`

func main() {
	if !validPort(socksPort) {
		fmt.Fprintf(os.Stderr, "MYSTUI_PORT must be a port number from 1 to 65535, got %q\n", socksPort)
		os.Exit(2)
	}
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-v", "--version":
			fmt.Println("mystui", version)
		case "help", "-h", "--help":
			fmt.Print(usage)
		default:
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
		return
	}
	if _, err := tea.NewProgram(model{}, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
