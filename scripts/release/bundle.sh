#!/bin/sh
# The stack release bundle: every file an install needs from this repository
# at runtime, with its manifest (docs/updates.md "Stack releases").
#
#   scripts/release/bundle.sh --out DIR [--version V] [--release] [--dist DIST]
#
#   --out DIR      write the bundle tree to DIR (created; must be empty)
#   --version V    the stack version (default 0.0.0-dev); written to VERSION
#   --release      pin the images this repository builds (stack-ergo,
#                  stack-certsync, stack-webircgateway) to :V in the
#                  compose files, as a release does; without it they keep
#                  the tags compose.yaml names on main
#   --dist DIST    also write DIST/boson-stack-V.tar.gz, its .sha256 and
#                  DIST/manifest.json (the GitHub release assets)
#   --commit SHA   recorded in the manifest (default: git rev-parse HEAD)
#
# The tree has the install's files at their paths plus .bundle/:
# manifest.json (version, each service's default image, the minimum admin
# panel, the files with their sha256, the migrations) and files.sha256
# (sha256sum format, what scripts/stack-apply.sh records as installed).
# POSIX sh + awk; runs on the CI runner and in the setup image's build.
set -eu

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
OUT=
VERSION=0.0.0-dev
RELEASE=false
DIST=
COMMIT=

die() { printf 'bundle.sh: %s\n' "$*" >&2; exit 1; }

while [ $# -gt 0 ]; do
	case "$1" in
	--out) OUT=${2:?--out needs a directory}; shift ;;
	--version) VERSION=${2:?--version needs a version}; shift ;;
	--release) RELEASE=true ;;
	--dist) DIST=${2:?--dist needs a directory}; shift ;;
	--commit) COMMIT=${2:?--commit needs a sha}; shift ;;
	-h | --help) sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
	*) die "unknown option: $1" ;;
	esac
	shift
done
[ -n "$OUT" ] || die "--out is required"
VERSION=${VERSION#v}
case "$VERSION" in
*[!0-9A-Za-z.+-]* | '') die "bad version: $VERSION" ;;
esac
if [ -z "$COMMIT" ]; then
	COMMIT=$(git -C "$ROOT" rev-parse HEAD 2>/dev/null || echo unknown)
fi
if [ -d "$OUT" ] && [ -n "$(ls -A "$OUT" 2>/dev/null)" ]; then
	die "$OUT is not empty"
fi
mkdir -p "$OUT"
OUT=$(cd "$OUT" && pwd)

sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$1" | awk '{ print $1 }'
	else
		shasum -a 256 "$1" | awk '{ print $1 }'
	fi
}

# ---- the files ----------------------------------------------------------------
# Paths (files or directories) relative to the repository. Directories are
# copied whole, minus tests (*_test.go, testdata/), caches and editor files.
FILES="
compose.yaml
compose.video-node.yaml
.env.example
README.md
LICENSE
NOTICE
config/boson.example.yaml
config/branding/.gitkeep
traefik/dynamic/stack.yml
traefik/dynamic/directory.yml
traefik/dynamic/README.md
postgres
monitoring
scripts/setup.sh
scripts/migrate-config.sh
scripts/stack-apply.sh
migrations
docs
images/certsync
images/ergo
images/webircgateway
images/wellknown
"

list_files() {
	for p in $FILES; do
		if [ -d "$ROOT/$p" ]; then
			(cd "$ROOT" && find "$p" -type f \
				! -name '*_test.go' ! -path '*/testdata/*' ! -path '*/__pycache__/*' \
				! -path '*/node_modules/*' ! -name '.DS_Store' ! -name '*.swp' ! -name '*~')
		elif [ -f "$ROOT/$p" ]; then
			echo "$p"
		else
			die "missing: $p"
		fi
	done | LC_ALL=C sort -u
}

for f in $(list_files); do
	case "$f" in
	*' '*) die "a path with a space: $f" ;;
	esac
	mkdir -p "$OUT/$(dirname "$f")"
	cp -p "$ROOT/$f" "$OUT/$f"
done
printf '%s\n' "$VERSION" > "$OUT/VERSION"
chmod 644 "$OUT/VERSION"

# A release pins the images this repository builds to its version.
if $RELEASE; then
	for f in compose.yaml compose.video-node.yaml; do
		sed -E "s#(harbor\.floret\.dev/boson/stack-(ergo|certsync|webircgateway|setup)):[A-Za-z0-9._-]+#\1:$VERSION#g" \
			"$OUT/$f" > "$OUT/$f.tmp"
		cat "$OUT/$f.tmp" > "$OUT/$f"
		rm -f "$OUT/$f.tmp"
	done
fi

# ---- the manifest -------------------------------------------------------------
mkdir -p "$OUT/.bundle"
(cd "$OUT" && find . -type f ! -path './.bundle/*' | sed 's#^\./##' | LC_ALL=C sort) > "$OUT/.bundle/files.list"
: > "$OUT/.bundle/files.sha256"
while read -r f; do
	printf '%s  %s\n' "$(sha256 "$OUT/$f")" "$f" >> "$OUT/.bundle/files.sha256"
done < "$OUT/.bundle/files.list"
rm -f "$OUT/.bundle/files.list"

# services: name → default image (${X_IMAGE:-ref} or the literal image:),
# the variable, whether this repository builds it, one-shot (restart "no"),
# its profiles and the files it bind-mounts (./path sources).
services_json() {
	awk -v file="$1" '
	function flush() {
		if (svc == "") return
		if (n++) printf ",\n"
		printf "    \"%s\": {\"compose\": \"%s\", \"image\": \"%s\"", svc, file, image
		if (envvar != "") printf ", \"env\": \"%s\"", envvar
		if (built) printf ", \"built\": true"
		if (oneshot) printf ", \"oneshot\": true"
		printf ", \"profiles\": [%s]", profiles
		printf ", \"mounts\": [%s]}", mounts
		svc = ""
	}
	/^[^ #]/ { flush(); top = $0; sub(/:.*/, "", top); next }
	top == "services" && /^  [A-Za-z0-9_.-]+:[ \t]*$/ {
		flush(); svc = $1; sub(/:$/, "", svc)
		image = ""; envvar = ""; built = 0; oneshot = 0; profiles = ""; mounts = ""; next
	}
	svc == "" { next }
	/^    image:/ {
		v = $0; sub(/^    image:[ \t]*/, "", v); gsub(/["\047]/, "", v)
		if (match(v, /^\$\{[A-Z0-9_]+:?-/)) {
			envvar = substr(v, 3); sub(/:?-.*/, "", envvar)
			sub(/^\$\{[A-Z0-9_]+:?-/, "", v); sub(/\}$/, "", v)
		}
		image = v; next
	}
	/^    build:/ { built = 1; next }
	/^    restart:[ \t]*["\047]?no["\047]?[ \t]*$/ { oneshot = 1; next }
	/^    profiles:/ {
		v = $0; sub(/^    profiles:[ \t]*\[/, "", v); sub(/\].*/, "", v); gsub(/[ \t"\047]/, "", v)
		k = split(v, ps, ",")
		for (i = 1; i <= k; i++) if (ps[i] != "") profiles = profiles (profiles == "" ? "" : ", ") "\"" ps[i] "\""
		next
	}
	/^      - \.\/[^:]+:/ {
		v = $0; sub(/^      - \.\//, "", v); sub(/:.*/, "", v)
		mounts = mounts (mounts == "" ? "" : ", ") "\"" v "\""
		next
	}
	END { flush(); if (n) printf "\n" }
	' "$OUT/$1"
}

min_admin=$(tr -d ' \n' < "$ROOT/scripts/release/min-admin-version")
{
	printf '{\n  "schema": 1,\n  "version": "%s",\n  "commit": "%s",\n' "$VERSION" "$COMMIT"
	printf '  "release": %s,\n' "$RELEASE"
	printf '  "min_admin": "%s",\n' "$min_admin"
	printf '  "services": {\n'
	services_json compose.yaml
	printf '  },\n  "video_node_services": {\n'
	services_json compose.video-node.yaml
	printf '  },\n  "migrations": ['
	first=true
	for m in "$OUT"/migrations/[0-9][0-9][0-9][0-9]-*.sh; do
		[ -f "$m" ] || continue
		$first || printf ', '
		first=false
		printf '"%s"' "$(basename "$m")"
	done
	printf '],\n  "files": {\n'
	awk '{ printf "%s    \"%s\": \"%s\"", (NR > 1 ? ",\n" : ""), $2, $1 } END { if (NR) printf "\n" }' "$OUT/.bundle/files.sha256"
	printf '  }\n}\n'
} > "$OUT/.bundle/manifest.json"

if [ -n "$DIST" ]; then
	mkdir -p "$DIST"
	DIST=$(cd "$DIST" && pwd)
	tgz="$DIST/boson-stack-$VERSION.tar.gz"
	(cd "$OUT" && tar -czf "$tgz" .)
	(cd "$DIST" && printf '%s  %s\n' "$(sha256 "$tgz")" "boson-stack-$VERSION.tar.gz" > "boson-stack-$VERSION.tar.gz.sha256")
	cp "$OUT/.bundle/manifest.json" "$DIST/manifest.json"
	echo "$tgz"
fi
