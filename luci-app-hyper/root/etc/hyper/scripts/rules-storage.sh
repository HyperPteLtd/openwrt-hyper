#!/bin/sh
# Keep only the server rule directory readable by the unprivileged core.
# publish receives a complete JSON file on stdin and never exposes a partial file.
set -eu

RULES_DIR="${HYPER_RULES_DIR:-/etc/hyper/rules}"

directory() {
	[ ! -L "$RULES_DIR" ] || { echo 'Rules directory must not be a symlink' >&2; return 1; }
	if [ ! -d "$RULES_DIR" ]; then
		mkdir -m 700 "$RULES_DIR"
	fi
	chown root:sing-box "$RULES_DIR"
	chmod 750 "$RULES_DIR"
}

case "${1:-}" in
	prepare)
		directory
		for file in "$RULES_DIR"/*.json; do
			[ -e "$file" ] || [ -L "$file" ] || continue
			[ ! -L "$file" ] && [ -f "$file" ] && [ "$(ls -ld "$file" | awk '{print $2; exit}')" = 1 ] || {
				echo 'Rules must be regular files without links' >&2; exit 1;
			}
			chown root:sing-box "$file"
			chmod 640 "$file"
		done
		;;
	publish)
		name="${2:-}"
		case "$name" in
			''|*[!A-Za-z0-9_.-]*) echo 'Invalid rule filename' >&2; exit 1 ;;
			*.json) ;;
			*) echo 'Invalid rule filename' >&2; exit 1 ;;
		esac
		directory
		target="$RULES_DIR/$name"
		if [ -e "$target" ] || [ -L "$target" ]; then
			[ ! -L "$target" ] && [ -f "$target" ] || {
				echo 'Rule destination must be a regular file' >&2; exit 1;
			}
		fi
		tmp="$(mktemp "$RULES_DIR/.rule.XXXXXX")"
		trap 'rm -f "$tmp"' 0
		trap 'exit 130' INT
		trap 'exit 143' TERM
		cat > "$tmp"
		chown root:sing-box "$tmp"
		chmod 640 "$tmp"
		mv -f "$tmp" "$target"
		;;
	*) echo 'Usage: rules-storage.sh prepare | publish <name.json>' >&2; exit 1 ;;
esac
