#!/bin/sh
# Keep the caller's PID as the session/group ID on every supported firmware.
# OpenWrt 23.05 omits the BusyBox setsid applet; the bundled helper fills that gap.
[ "$#" -gt 0 ] || exit 2
if command -v setsid >/dev/null 2>&1; then
	exec setsid "$@"
fi
exec /usr/libexec/hyper/bin/hyper-crypt run-session "$@"
