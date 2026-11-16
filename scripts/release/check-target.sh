#!/bin/sh
# Only publish automatic releases from the current main commit. A newer push
# can change workflows during a build, which GITHUB_TOKEN cannot tag/release
# from the older commit. Never attach old packages to the new commit instead.
set -eu

: "${RELEASE_TAG:?}" "${GITHUB_REF:?}" "${GITHUB_OUTPUT:?}"
sh "$(dirname "$0")/validate-tag.sh" "$RELEASE_TAG"
git check-ref-format "refs/tags/$RELEASE_TAG"
commit="$(git rev-parse HEAD)"

if [ "$GITHUB_REF" = refs/heads/main ]; then
	heads="$(git ls-remote --exit-code origin refs/heads/main)"
	latest="$(printf '%s\n' "$heads" | cut -f1)"
	[ -n "$latest" ] || { echo '::error::Could not resolve main'; exit 1; }
	if [ "$latest" != "$commit" ]; then
		echo 'publish=false' >> "$GITHUB_OUTPUT"
		message="Skipping $RELEASE_TAG: main advanced during this build; the newer run will publish its own packages"
		echo "::notice::$message"
		if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
			printf '%s\n' "$message" >> "$GITHUB_STEP_SUMMARY"
		fi
		exit 0
	fi
fi

ref="refs/tags/$RELEASE_TAG"
# Capture first so a failed network request cannot look like a missing tag.
refs="$(git ls-remote --tags origin "$ref" "$ref^{}")"
tagged_commit="$(printf '%s\n' "$refs" |
	awk '$2 ~ /\^\{\}$/ { peeled=$1; next } { sha=$1 } END { print peeled ? peeled : sha }')"
case "$GITHUB_REF" in
	refs/tags/*)
		[ -n "$tagged_commit" ] || { echo "::error::$RELEASE_TAG no longer exists on origin"; exit 1; }
		;;
esac
if [ -n "$tagged_commit" ] && [ "$tagged_commit" != "$commit" ]; then
	echo "::error::$RELEASE_TAG already points to a different commit"
	exit 1
fi
printf 'publish=true\ncommit=%s\n' "$commit" >> "$GITHUB_OUTPUT"
