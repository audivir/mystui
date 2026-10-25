# mystui

A terminal front end for the [Mysterium VPN CLI](https://github.com/mysteriumnetwork/mysteriumvpncli).
It runs `mystvpn` and a SOCKS5 proxy in a Docker container, so the VPN only carries the traffic
you send through the proxy. The rest of your machine stays outside the tunnel, which gives you
split tunneling without a split-tunnel feature in the VPN itself.
The interface is built with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

```
Mysterium VPN

container up   socks5 open 127.0.0.1:1080
vpn connected (residential)   exit 203.0.113.7  Berlin 🇩🇪 DE
────────────────────────────────────────────────────────────────
Countries  residential IPs  52 available
 🇦🇱 Albania      🇦🇷 Argentina    🇦🇺 Australia    🇦🇹 Austria
●🇩🇪 Germany      🇬🇷 Greece       🇭🇺 Hungary      🇮🇪 Ireland
 ...

enter connect   d disconnect   f refresh   t ip type   a sign in   L sign out
u start container   x stop container   c chrome via proxy   ? short help   q quit
```

## Prerequisites

- A Mysterium VPN subscription.
- Docker with Compose. The Docker host needs a kernel with WireGuard: a Linux host, or on macOS a VM
  such as [Colima](https://github.com/abiosoft/colima). Docker Desktop is untested.
- Go, only to build the binary.
- Optional: Google Chrome or Chromium, for the `c` key.

## Installation

```sh
go install github.com/audivir/mystui@latest
```

Or from a clone:

```sh
git clone https://github.com/audivir/mystui
cd mystui
go build -o ~/.local/bin/mystui .
```

Binaries for Linux, macOS, and Windows are attached to each GitHub release.

Make sure the install directory (`~/.local/bin`, or `$(go env GOPATH)/bin` for `go install`) is on your `PATH`.

## Usage

### First run

```sh
mystui
```

1. Press `u`. The first run builds the container image, which takes a few minutes.
   The Dockerfile and compose file are embedded in the binary and written to `~/.local/share/mystui` (or below `$XDG_DATA_HOME`).
1. Press `a`. The sign-in URL is printed; open it in your browser and approve. The link expires after 5 minutes.
1. Pick a country with the arrow keys and press `enter`.
1. Use the proxy at `socks5h://127.0.0.1:1080` (set `MYSTUI_PORT` to change the port), or press `c` to open Chrome through it.

The `h` in `socks5h` makes DNS lookups go through the proxy as well. Example:

```sh
curl -x socks5h://127.0.0.1:1080 https://api.ipify.org
```

### Keys

| Key | Action |
| --- | --- |
| `←` `↑` `↓` `→` or `h` `j` `k` `l` | move through the countries |
| `enter` | connect to the selected country |
| `d` | disconnect |
| `f` | refresh (reconnect with the saved country) |
| `t` | switch between residential and hosting IPs |
| `a` / `L` | sign in / sign out |
| `u` / `x` | start / stop the container |
| `c` | open Chrome through the proxy |
| `C` | clear the separate Chrome profile (asks first) |
| `r` | reload |
| `?` | show all keys or the short list |
| `q` | quit |

### Chrome

`c` opens Chrome with the proxy flags on your normal profile, so your logins and extensions are available.
Chrome applies `--proxy-server` only when it starts a new instance. If Chrome is already running on that profile,
the TUI asks whether to open a separate proxied profile (`~/.local/share/mystui/chrome`) instead.
`C` deletes only that separate profile, never your normal one.

### Configuration

| Variable | Meaning | Default |
| --- | --- | --- |
| `MYSTUI_DIR` | directory for the generated files, the data, and the Chrome profile | `$XDG_DATA_HOME/mystui`, else `~/.local/share/mystui` |
| `MYSTUI_PORT` | host port of the SOCKS5 proxy; it is only published on `127.0.0.1` | `1080` |
| `MYSTUI_CHROME_PROFILE` | Chrome profile that `c` opens | your normal Chrome profile |
| `MYSTUI_FLAGS` | `1` or `0` forces flag emoji on or off | on for UTF-8 locales |

Changing `MYSTUI_PORT` takes effect when the container is recreated: press `u`.

### Where things live

| Path | Contents |
| --- | --- |
| `~/.local/share/mystui/docker/` | `Dockerfile` and `compose.yaml`, rewritten from the binary on every start and stop |
| `~/.local/share/mystui/data/` | sign-in tokens and the WireGuard keypair, mounted into the container; keep it private |
| `~/.local/share/mystui/chrome/` | the separate Chrome profile, created on demand |
| `~/.local/share/mystui/iptype` | the IP type of the current connection, which the VPN CLI does not report |

To remove everything: stop the container with `x`, run `docker rmi` on the built image, and delete `~/.local/share/mystui` (or below `$XDG_DATA_HOME`).

### Limitations

- The VPN CLI chooses the city; you can only pick the country and the IP type.
- The country list needs a signed-in session. Without one the TUI says so.
- On Windows, the check for a running Chrome only sees the normal profile.
- The `mystvpn` version is pinned in `docker/Dockerfile` (`MYSTVPN_VERSION`).

## License

[MIT](LICENSE). The Mysterium VPN CLI that runs inside the container is a separate program under its own license (GPL-2.0).
