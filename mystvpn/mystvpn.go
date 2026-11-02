// Package mystvpn stores the helpers that mystui and mystgw share to drive mystvpn in a container.
package mystvpn

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// MaxHandshakeAge is the oldest handshake of the tunnel that counts as up. mystvpn sends a
// keepalive every 25 seconds, while `mystvpn status` shows the saved state even when the exit node
// has stopped answering.
const MaxHandshakeAge = 5 * time.Minute

var authRe = regexp.MustCompile(`authentication required|not signed in|auth`)

// Status stores the fields of `mystvpn status`.
type Status struct {
	Connected bool
	IP        string
	Country   string
	City      string
}

// Tunnel stores the WireGuard interface of mystvpn and the age of its newest handshake.
type Tunnel struct {
	Interface string
	Age       time.Duration
	// Handshaken is false if the interface has not completed a handshake yet.
	Handshaken bool
}

// Up reports whether the tunnel has an interface and a recent handshake.
func (t Tunnel) Up() bool {
	return t.Interface != "" && t.Handshaken && t.Age <= MaxHandshakeAge
}

// Client runs commands in the container of mystvpn through Run, which executes a command and
// returns its trimmed combined output.
type Client struct {
	Container string
	Run       func(name string, args ...string) (string, error)
}

// Exec runs a command in the container.
func (c *Client) Exec(args ...string) (string, error) {
	return c.Run("docker", append([]string{"exec", c.Container}, args...)...)
}

// VPN runs mystvpn in the container.
func (c *Client) VPN(args ...string) (string, error) {
	return c.Exec(append([]string{"mystvpn"}, args...)...)
}

// Status reads `mystvpn status` and returns the raw output next to the parsed fields.
func (c *Client) Status() (Status, string, error) {
	out, err := c.VPN("status")
	return ParseStatus(out), out, err
}

// Connect connects to a country with an IP type and returns the output of mystvpn.
func (c *Client) Connect(country, ipType string) (string, error) {
	return c.VPN("connect", "--country", country, "--ip-type", ipType)
}

// Reconnect replaces a dead tunnel. A dead tunnel keeps the default route, so every request of
// mystvpn enters it and fails, including disconnect, connect, and refresh. Deleting the interface
// first removes its routes. Traffic leaves outside the tunnel until the connect finishes.
func (c *Client) Reconnect(country, ipType, iface string) (string, error) {
	// each step may fail because there is nothing to tear down, so connect still runs.
	if iface != "" {
		_, _ = c.Exec("ip", "link", "delete", iface)
	}
	_, _ = c.VPN("disconnect")
	return c.Connect(country, ipType)
}

// Tunnel finds the WireGuard interface of mystvpn, which is the one not named in exclude, and the
// age of its newest handshake.
func (c *Client) Tunnel(now time.Time, exclude ...string) Tunnel {
	links, _ := c.Exec("ip", "-o", "link", "show", "type", "wireguard")
	t := Tunnel{Interface: TunnelInterface(links, exclude...)}
	if t.Interface == "" {
		return t
	}
	out, _ := c.Exec("wg", "show", t.Interface, "latest-handshakes")
	t.Age, t.Handshaken = HandshakeAge(out, now)
	return t
}

// ParseStatus reads the lines of `mystvpn status`.
func ParseStatus(out string) Status {
	var s Status
	for _, l := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(l, ":")
		if !ok {
			continue
		}
		v = strings.TrimSpace(v)
		switch strings.TrimSpace(k) {
		case "connected":
			s.Connected = v == "yes"
		case "IP address":
			s.IP = v
		case "country":
			s.Country = v
		case "city":
			s.City = v
		}
	}
	return s
}

// AuthRequired reports whether the output of mystvpn says that no sign-in exists.
func AuthRequired(out string) bool { return authRe.MatchString(out) }

// TunnelInterface returns the first WireGuard interface in the output of
// `ip -o link show type wireguard` that is not named in exclude.
func TunnelInterface(links string, exclude ...string) string {
	for _, l := range strings.Split(links, "\n") {
		f := strings.Fields(l)
		if len(f) < 2 {
			continue
		}
		name := strings.TrimSuffix(strings.SplitN(f[1], "@", 2)[0], ":")
		if !contains(exclude, name) {
			return name
		}
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

// HandshakeAge returns the age of the newest handshake in the output of
// `wg show <interface> latest-handshakes`, and false if no peer has completed one.
func HandshakeAge(out string, now time.Time) (time.Duration, bool) {
	newest := int64(0)
	for _, l := range strings.Split(out, "\n") {
		f := strings.Fields(l)
		if len(f) != 2 {
			continue
		}
		if t, err := strconv.ParseInt(f[1], 10, 64); err == nil && t > newest {
			newest = t
		}
	}
	if newest == 0 {
		return 0, false
	}
	return now.Sub(time.Unix(newest, 0)), true
}

// LastLine returns the last line of a command output.
func LastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// Describe formats the tunnel for messages, e.g. for a dead tunnel.
func (t Tunnel) Describe() string {
	switch {
	case t.Interface == "":
		return "no WireGuard interface of mystvpn"
	case !t.Handshaken:
		return "no handshake with the exit node"
	case t.Age > MaxHandshakeAge:
		return fmt.Sprintf("last handshake %s ago, the exit node does not answer", t.Age.Round(time.Second))
	}
	return "up"
}

//go:embed docker/watchdog.sh docker/mystvpn-wrapper.sh docker/killswitch.sh
var scripts embed.FS

// WriteScripts writes watchdog.sh, mystvpn-wrapper.sh, and killswitch.sh into a Docker build context, where the
// Dockerfile copies them next to mystvpn.
func WriteScripts(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"watchdog.sh", "mystvpn-wrapper.sh", "killswitch.sh"} {
		b, err := scripts.ReadFile("docker/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o755); err != nil {
			return err
		}
	}
	return nil
}
