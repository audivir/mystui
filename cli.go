package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// runCLI runs the command line version of a key of the interface and returns the exit code.
// Every command prints what the key would show in the footer, and fails with exit code 1 when the
// key would show a failure, so scripts and SSH sessions can use them.
func runCLI(args []string, in io.Reader, out, errw io.Writer) int {
	cmd, rest := args[0], args[1:]
	usageErr := func(msg string) int {
		sayf(errw, "mystui %s: %s\n", cmd, msg)
		return 2
	}
	if cmd != "connect" && cmd != "clear" && len(rest) > 0 {
		return usageErr("takes no arguments")
	}
	switch cmd {
	case "status":
		return cliStatus(out)
	case "connect":
		return cliConnect(rest, out, errw)
	case "disconnect":
		return cliDone(simple("disconnect", "disconnect"), out, errw)
	case "signin":
		return cliSignin(out, errw)
	case "signout":
		return cliDone(simple("sign out", "logout"), out, errw)
	case "up":
		return cliDone(compose("up", "-d", "--build"), out, errw)
	case "down":
		return cliDone(compose("down"), out, errw)
	case "chrome":
		profile := chromeProfile
		if chromeRunningFn(profile) { // a running Chrome ignores the proxy flags, and there is nobody to ask.
			profile = bundledProfile
			sayf(out, "chrome is already running on %s, using the separate profile %s\n", chromeProfile, profile)
		}
		return cliDone(chrome(profile), out, errw)
	case "clear":
		return cliClear(rest, in, out, errw)
	}
	return usageErr("unknown command")
}

// cliDone runs the command of a key and prints its result.
func cliDone(cmd tea.Cmd, out, errw io.Writer) int {
	msg := cmd().(doneMsg)
	if msg.failed {
		sayln(errw, msg.text)
		return 1
	}
	sayln(out, msg.text)
	return 0
}

// cliStatus prints the state of the container, the proxy and the VPN, and fails unless the VPN is connected.
func cliStatus(out io.Writer) int {
	st := vpn(fetchStatus().(statusMsg))
	yesNo := func(ok bool, yes, no string) string {
		if ok {
			return yes
		}
		return no
	}
	sayf(out, "container: %s\n", yesNo(st.containerUp, "up", "down"))
	sayf(out, "proxy:     %s\n", yesNo(st.socksUp, "listening on "+socksAddr, "not listening"))
	switch {
	case st.connected:
		sayf(out, "vpn:       connected  %s %s  %s  %s\n", st.country, st.city, st.ip, st.ipType)
	case st.lost != "":
		sayf(out, "vpn:       lost  %s\n", st.lost)
	default:
		sayln(out, "vpn:       disconnected")
	}
	if st.connected {
		return 0
	}
	return 1
}

// cliConnect parses `[--ip-type TYPE] COUNTRY` and connects, or reconnects a lost tunnel.
func cliConnect(args []string, out, errw io.Writer) int {
	ipType, code := ipTypes[0], ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--ip-type" && i+1 < len(args):
			i++
			ipType = args[i]
		case strings.HasPrefix(a, "--ip-type="):
			ipType = strings.TrimPrefix(a, "--ip-type=")
		case code == "" && !strings.HasPrefix(a, "-"):
			code = strings.ToUpper(a)
		default:
			sayf(errw, "mystui connect: unexpected %q\n", a)
			return 2
		}
	}
	if !codeRe.MatchString(code) || !slices.Contains(ipTypes, ipType) {
		sayf(errw, "usage: mystui connect [--ip-type %s] COUNTRY (two letters, like AT)\n", strings.Join(ipTypes, "|"))
		return 2
	}
	// a lost tunnel makes every request of a plain connect fail, so it has to be replaced.
	if st := vpn(fetchStatus().(statusMsg)); st.lost != "" {
		return cliDone(reconnect(code, ipType, st.iface), out, errw)
	}
	return cliDone(connect(code, ipType), out, errw)
}

// cliSignin hands the terminal to `mystvpn auth`, which prints the approval URL.
func cliSignin(out, errw io.Writer) int {
	c := exec.Command("docker", "exec", "-it", container, "mystvpn", "auth")
	c.Stdin, c.Stdout, c.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := c.Run(); err != nil {
		sayln(errw, "sign-in failed: "+err.Error())
		return 1
	}
	sayln(out, "signed in")
	return 0
}

// cliClear deletes the separate Chrome data after a yes, which -y gives in advance.
func cliClear(args []string, in io.Reader, out, errw io.Writer) int {
	yes := len(args) == 1 && (args[0] == "-y" || args[0] == "--yes")
	if len(args) > 0 && !yes {
		sayln(errw, "usage: mystui clear [-y]")
		return 2
	}
	size, err := dirSize(bundledProfile)
	if err != nil {
		sayln(out, "no separate chrome data yet")
		return 0
	}
	if !yes {
		sayf(out, "Delete %s (%s)? Logins, cookies, history and extensions in it are lost. [y/N] ", bundledProfile, humanSize(size))
		line, _ := bufio.NewReader(in).ReadString('\n')
		if strings.ToLower(strings.TrimSpace(line)) != "y" {
			sayln(out, "cancelled")
			return 1
		}
	}
	return cliDone(clearChrome(), out, errw)
}

// cliCommands lists the commands of runCLI, as main dispatches on them.
var cliCommands = []string{"status", "connect", "disconnect", "signin", "signout", "up", "down", "chrome", "clear"}

// sayf and sayln write to the output of a command, where a failed write has nobody to tell.
func sayf(w io.Writer, format string, a ...any) { _, _ = fmt.Fprintf(w, format, a...) }

func sayln(w io.Writer, a ...any) { _, _ = fmt.Fprintln(w, a...) }
