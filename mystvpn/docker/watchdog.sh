#!/bin/sh
# keeps the tunnel of mystvpn alive: when the newest handshake is older than WATCHDOG_MAX_AGE
# seconds, or the interface is gone, it deletes the dead interface and connects again with the
# arguments that mystvpn-wrapper.sh recorded for the last connect. A dead tunnel keeps the default
# route, so the requests of mystvpn only work once the interface is deleted.
#
# A tunnel can shake hands and still carry no traffic, e.g. when the exit node has lost its uplink.
# So every WATCHDOG_INTERVAL seconds the watchdog also sends a packet through the tunnel with
# WATCHDOG_PROBE, and after WATCHDOG_PROBE_FAILS failures in a row it connects again. Two connects
# are at least WATCHDOG_COOLDOWN seconds apart, so a broken uplink of the host does not hammer the
# API of mystvpn. An empty WATCHDOG_PROBE turns the probe off.
#
# After the host slept, the exit node may still know the tunnel. A packet then starts a new
# handshake and the exit address stays, so the watchdog sends one first and waits before it gives up.
set -u

interval="${WATCHDOG_INTERVAL:-3}"
max_age="${WATCHDOG_MAX_AGE:-120}"
nudge="${WATCHDOG_NUDGE:-ping -c 2 -W 3 1.1.1.1}"
nudge_wait="${WATCHDOG_NUDGE_WAIT:-8}"
grace="${WATCHDOG_GRACE:-60}"
exclude="${WATCHDOG_EXCLUDE:-wg0}"
probe="${WATCHDOG_PROBE-ping -c 1 -W 1 1.1.1.1 || ping -c 1 -W 1 8.8.8.8}"
probe_fails="${WATCHDOG_PROBE_FAILS:-3}"
cooldown="${WATCHDOG_COOLDOWN:-30}"
rounds="${WATCHDOG_ROUNDS:-0}"
real="${MYSTVPN_REAL:-/usr/local/bin/mystvpn.real}"
wrapper="${MYSTVPN:-mystvpn}"
last="${DATA_DIR:-/data}/.last-connect"
pause=0
fails=0
replaced_at=0
round=0

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
  round=$((round + 1))
  if [ -f "$last" ]; then
    read -r connected_at args <"$last"
    iface="$(tunnel_interface)"
    replaced=0
    # a tunnel with a fresh handshake that carries no traffic is dead too
    if [ -n "$probe" ] && [ -n "$iface" ] && [ "$(($(date +%s) - connected_at))" -gt "$grace" ]; then
      if sh -c "$probe" >/dev/null 2>&1; then
        fails=0
      else
        fails=$((fails + 1))
      fi
      if [ "$fails" -ge "$probe_fails" ]; then
        fails=0
        if [ "$(($(date +%s) - replaced_at))" -ge "$cooldown" ]; then
          log "no traffic through $iface in $probe_fails probes"
          replace_tunnel "$iface" "$args"
          replaced=1
          replaced_at="$(date +%s)"
        fi
      fi
    fi
    # a tunnel that was connected just now may not have shaken hands yet, but one that is gone
    # has nothing to wait for
    if [ "$replaced" -eq 0 ] && { [ -z "$iface" ] || [ "$(($(date +%s) - connected_at))" -gt "$grace" ]; } && is_dead "$iface"; then
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
  [ "$rounds" -gt 0 ] && [ "$round" -ge "$rounds" ] && exit 0
  sleep "$((interval + pause))"
done
