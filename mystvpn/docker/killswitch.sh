#!/bin/sh
# keeps the proxy from leaking: the traffic of the proxy user may only leave through the tunnel of
# mystvpn, so a dead tunnel or a reconnect cuts it off instead of sending it out with the address
# of the host. The rules match the interface name with a wildcard, so they hold before the tunnel
# exists and while it is replaced. mystvpn and the watchdog run as root and stay unaffected.
#
# KILLSWITCH_USER       user of the proxy (default proxy)
# KILLSWITCH_ALLOW      interfaces the user may send through (default lo mvpn+)
# KILLSWITCH_SPORT      TCP port of the proxy, whose replies to the host may leave through any interface
# KILLSWITCH_FORWARD_IN interface whose forwarded traffic has to leave through the tunnel too
set -eu

user="${KILLSWITCH_USER:-proxy}"
allow="${KILLSWITCH_ALLOW:-lo mvpn+}"
sport="${KILLSWITCH_SPORT:-}"
forward_in="${KILLSWITCH_FORWARD_IN:-}"

for ipt in iptables ip6tables; do
  command -v "$ipt" >/dev/null 2>&1 || continue
  # the chain is rebuilt on every start
  "$ipt" -N KILLSWITCH 2>/dev/null || "$ipt" -F KILLSWITCH
  for iface in $allow; do
    "$ipt" -A KILLSWITCH -o "$iface" -j ACCEPT
  done
  if [ -n "$sport" ]; then
    "$ipt" -A KILLSWITCH -p tcp --sport "$sport" -j ACCEPT
  fi
  "$ipt" -A KILLSWITCH -j REJECT
  "$ipt" -C OUTPUT -m owner --uid-owner "$user" -j KILLSWITCH 2>/dev/null ||
    "$ipt" -I OUTPUT 1 -m owner --uid-owner "$user" -j KILLSWITCH
  if [ -n "$forward_in" ]; then
    "$ipt" -C FORWARD -i "$forward_in" ! -o mvpn+ ! -o "$forward_in" -j REJECT 2>/dev/null ||
      "$ipt" -I FORWARD 1 -i "$forward_in" ! -o mvpn+ ! -o "$forward_in" -j REJECT
  fi
done
