package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cli runs a command line and returns its exit code and both outputs.
func cli(t *testing.T, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = runCLI(args, strings.NewReader(stdin), &o, &e)
	return code, o.String(), e.String()
}

func cliEnv(t *testing.T, wg string) string {
	t.Helper()
	projectDir = t.TempDir()
	// composeFile is computed at start, so it would still point at the real compose file.
	composeFile = filepath.Join(projectDir, "docker", "compose.yaml")
	fakeDocker(t, wg)
	log := filepath.Join(t.TempDir(), "calls")
	t.Setenv("FAKE_LOG", log)
	return log
}

func TestCLIStatus(t *testing.T) {
	cliEnv(t, "3: wg0: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420\n")
	code, out, _ := cli(t, "", "status")
	if code != 0 || !strings.Contains(out, "container: up") || !strings.Contains(out, "connected  AT salzburg  1.2.3.4") {
		t.Fatalf("connected status: %d\n%s", code, out)
	}

	t.Setenv("FAKE_HS", fmt.Sprintf("KEY\t%d", time.Now().Add(-6*time.Hour).Unix()))
	if code, out, _ = cli(t, "", "status"); code != 1 || !strings.Contains(out, "vpn:       lost") {
		t.Fatalf("a lost tunnel must fail: %d\n%s", code, out)
	}

	cliEnv(t, "")
	if code, out, _ = cli(t, "", "status"); code != 1 || !strings.Contains(out, "disconnected") {
		t.Fatalf("no tunnel must fail: %d\n%s", code, out)
	}
}

func TestCLIConnect(t *testing.T) {
	log := cliEnv(t, "")
	code, out, _ := cli(t, "", "connect", "at")
	calls, _ := os.ReadFile(log)
	if code != 0 || !strings.Contains(out, "connected") || !strings.Contains(string(calls), "mystvpn connect --country AT --ip-type residential") {
		t.Fatalf("default type is residential: %d\n%s\n%s", code, out, calls)
	}
	cli(t, "", "connect", "--ip-type=hosting", "DE")
	cli(t, "", "connect", "FR", "--ip-type", "hosting")
	calls, _ = os.ReadFile(log)
	for _, want := range []string{"--country DE --ip-type hosting", "--country FR --ip-type hosting"} {
		if !strings.Contains(string(calls), want) {
			t.Fatalf("missing %q in\n%s", want, calls)
		}
	}
	for _, args := range [][]string{
		{"connect"}, {"connect", "AUT"}, {"connect", "--ip-type", "fast", "AT"}, {"connect", "AT", "DE"},
		{"connect", "--ip-type"}, {"connect", "--bogus", "AT"},
	} {
		if code, _, errOut := cli(t, "", args...); code != 2 || errOut == "" {
			t.Fatalf("%v must be a usage error, got %d %q", args, code, errOut)
		}
	}
	t.Setenv("FAKE_FAIL", "connect")
	if code, _, errOut := cli(t, "", "connect", "AT"); code != 1 || !strings.Contains(errOut, "connect failed") {
		t.Fatalf("a failed connect must exit 1: %d %q", code, errOut)
	}
}

func TestCLIConnectReplacesALostTunnel(t *testing.T) {
	log := cliEnv(t, "3: mvpn-1: <POINTOPOINT,NOARP,UP,LOWER_UP> mtu 1420\n")
	t.Setenv("FAKE_HS", fmt.Sprintf("KEY\t%d", time.Now().Add(-6*time.Hour).Unix()))
	if code, out, _ := cli(t, "", "connect", "AT"); code != 0 || !strings.Contains(out, "reconnected") {
		t.Fatalf("expected a reconnect: %d %s", code, out)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "ip link delete mvpn-1") {
		t.Fatalf("the dead interface must go first:\n%s", calls)
	}
}

func TestCLISimpleCommands(t *testing.T) {
	log := cliEnv(t, "")
	for _, c := range []struct{ args, want string }{
		{"disconnect", "mystvpn disconnect"}, {"signout", "mystvpn logout"}, {"signin", "mystvpn auth"},
		{"up", ""}, {"down", ""},
	} {
		if code, _, errOut := cli(t, "", c.args); code != 0 {
			t.Fatalf("%s: %d %s", c.args, code, errOut)
		}
		calls, _ := os.ReadFile(log)
		if !strings.Contains(string(calls), c.want) {
			t.Fatalf("%s: missing %q in\n%s", c.args, c.want, calls)
		}
	}
	for _, c := range []struct{ fail, cmd string }{{"disconnect", "disconnect"}, {"logout", "signout"}, {"compose", "up"}, {"compose", "down"}} {
		t.Setenv("FAKE_FAIL", c.fail)
		if code, _, errOut := cli(t, "", c.cmd); code != 1 || errOut == "" {
			t.Fatalf("%s must exit 1: %d %q", c.cmd, code, errOut)
		}
	}
	if code, _, _ := cli(t, "", "status", "extra"); code != 2 {
		t.Fatal("commands without arguments must reject them")
	}
	if code, _, _ := cli(t, "", "bogus"); code != 2 {
		t.Fatal("unknown command")
	}
}

func TestCLISigninFails(t *testing.T) {
	cliEnv(t, "")
	t.Setenv("PATH", t.TempDir()) // no docker at all.
	if code, _, errOut := cli(t, "", "signin"); code != 1 || !strings.Contains(errOut, "sign-in failed") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestCLIChrome(t *testing.T) {
	cliEnv(t, "")
	// the fakes stand in for `open` on macOS and for the browser on Linux.
	bin := t.TempDir()
	for _, name := range []string{"open", "google-chrome"} {
		_ = os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\nexit 0\n"), 0o755)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	defer func(f func(string) bool) { chromeRunningFn = f }(chromeRunningFn)

	chromeRunningFn = func(string) bool { return false }
	if code, out, _ := cli(t, "", "chrome"); code != 0 || !strings.Contains(out, "chrome started") {
		t.Fatalf("%d %s", code, out)
	}
	chromeRunningFn = func(string) bool { return true }
	bundledProfile = filepath.Join(t.TempDir(), "chrome")
	if code, out, _ := cli(t, "", "chrome"); code != 0 || !strings.Contains(out, "separate profile") {
		t.Fatalf("a running Chrome must switch to the separate profile: %d %s", code, out)
	}
	t.Setenv("PATH", t.TempDir()) // no browser.
	if code, _, errOut := cli(t, "", "chrome"); code != 1 || !strings.Contains(errOut, "chrome failed") {
		t.Fatalf("%d %q", code, errOut)
	}
}

func TestCLIClear(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "chrome")
	bundledProfile = dir
	if code, out, _ := cli(t, "", "clear"); code != 0 || !strings.Contains(out, "no separate chrome data") {
		t.Fatalf("nothing to clear: %d %s", code, out)
	}
	fill := func() { _ = os.MkdirAll(filepath.Join(dir, "Default"), 0o700) }

	fill()
	if code, out, _ := cli(t, "n\n", "clear"); code != 1 || !strings.Contains(out, "cancelled") {
		t.Fatalf("anything but y cancels: %d %s", code, out)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Fatal("cancel must keep the profile")
	}
	if code, out, _ := cli(t, "y\n", "clear"); code != 0 || !strings.Contains(out, "cleared") {
		t.Fatalf("%d %s", code, out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("profile should be gone")
	}
	fill()
	if code, _, _ := cli(t, "", "clear", "-y"); code != 0 {
		t.Fatal("-y deletes without asking")
	}
	if code, _, _ := cli(t, "", "clear", "--all"); code != 2 {
		t.Fatal("unknown option")
	}
	// the refusal only comes after the size check, so the normal profile has to exist, in a temporary home.
	defer func(h string) { home = h }(home)
	home = t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	bundledProfile = defaultProfile()
	_ = os.MkdirAll(bundledProfile, 0o700)
	if code, _, errOut := cli(t, "", "clear", "-y"); code != 1 || !strings.Contains(errOut, "refusing") {
		t.Fatalf("the normal profile must never be deleted: %d %q", code, errOut)
	}
}
