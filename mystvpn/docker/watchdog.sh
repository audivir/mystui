#!/bin/sh
# keeps the tunnel of mystvpn alive: when the newest handshake is older than WATCHDOG_MAX_AGE
# seconds, or the interface is gone, it deletes the dead interface and connects again with the
# arguments that mystvpn-wrapper.sh recorded for the last connect. A dead tunnel keeps the default
# route, so the requests of mystvpn only work once the interface is deleted.
#
# After the host slept, the exit node may still know the tunnel. A packet then starts a new
# handshake and the exit address stays, so the watchdog sends one first and waits before it gives up.
set -u

interval="${WATCHDOG_INTERVAL:-15}"
max_age="${WATCHDOG_MAX_AGE:-120}"
nudge="${WATCHDOG_NUDGE:-ping -c 2 -W 3 1.1.1.1}"
nudge_wait="${WATCHDOG_NUDGE_WAIT:-8}"
grace="${WATCHDOG_GRACE:-60}"
exclude="${WATCHDOG_EXCLUDE:-wg0}"
real="${MYSTVPN_REAL:-/usr/local/bin/mystvpn.real}"
wrapper="${MYSTVPN:-mystvpn}"
last="${DATA_DIR:-/data}/.last-connect"
pause=0

log() { echo "watchdog: $*"; }

# prints the WireGuard interface of mystvpn, which is not the one named in WATCHDOG_EXCLUDE
tunnel_interface() {
  ip -o link show type wireguard 2>/dev/null |
    awk -F': ' '{ split($2, n, "@"); print n[1] }' | grep -vx "$exclude" | head -n 1
}

# checks whether the interface is missing or its newest handshake is too old
is_dead() {
  [ -n "$1" ] || return 0
  newest="$(wg show "$1" latest-handshakes 2>/dev/null | awk '$2 > m { m = $2 } END { print m + 0 }')"
  [ "$(($(date +%s) - newest))" -gt "$max_age" ]
}

# connects again after deleting the dead interface
replace_tunnel() { # interface, arguments
  log "tunnel ${1:-missing} is dead, connecting again with: $2"
  [ -n "$1" ] && ip link delete "$1" 2>/dev/null
  "$real" disconnect >/dev/null 2>&1
  # the arguments hold no spaces, and the wrapper records the connect again
  # shellcheck disable=SC2086
  if "$wrapper" $2; then
    log "connected again"
    pause=0
  else
    log "connecting failed, trying again later"
    pause=60
  fi
}

while :; do
  if [ -f "$last" ]; then
    read -r connected_at args <"$last"
    iface="$(tunnel_interface)"
    # a tunnel that was connected just now may not have shaken hands yet
    if [ "$(($(date +%s) - connected_at))" -gt "$grace" ] && is_dead "$iface"; then
      if [ -n "$iface" ]; then
        sh -c "$nudge" >/dev/null 2>&1
        sleep "$nudge_wait"
      fi
      if is_dead "$iface"; then
        replace_tunnel "$iface" "$args"
      else
        log "tunnel $iface recovered by itself"
      fi
    fi
  fi
  [ -n "${WATCHDOG_ONCE:-}" ] && exit 0
  sleep "$((interval + pause))"
done
