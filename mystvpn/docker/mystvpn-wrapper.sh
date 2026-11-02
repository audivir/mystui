#!/bin/sh
# runs mystvpn and records the arguments of a successful connect for the watchdog; a disconnect or
# a logout removes them, so the watchdog leaves a tunnel down that a person took down
real="${MYSTVPN_REAL:-/usr/local/bin/mystvpn.real}"
last="${DATA_DIR:-/data}/.last-connect"

"$real" "$@"
rc=$?
case "${1:-}" in
connect)
  [ "$rc" -eq 0 ] && echo "$(date +%s) $*" >"$last"
  ;;
disconnect | logout)
  rm -f "$last"
  ;;
esac
exit "$rc"
