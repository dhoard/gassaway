#!/usr/bin/env bash
# Copyright (c) 2026-present Douglas Hoard. All rights reserved.
# Use of this source code is governed by the MIT license,
# which can be found in the LICENSE file.

# Install gassaway as a systemd system service.
#
# The installation has three parts with three lifecycles, and this script never
# mixes them:
#
#   program  <prefix>, default /opt/gassaway   root:root 0755, no state
#   facts    /etc/systemd/system/gassaway.service   generated from the repository
#            template, holds the address, the port and the data directory
#   state    /var/lib/gassaway                 gassaway:gassaway 0750, created by
#            systemd through StateDirectory=, never touched by this script
#
# Because the prefix holds no state, replacing it is a plain mv and removing it
# cannot destroy a graph. Because the unit holds the deployment facts, nothing
# here depends on the caller's working directory.
#
# Usage:
#   ./build.sh                    build and test as your own user
#   sudo ./install.sh             install and start the service on port 4029
#   sudo ./install.sh --port N    install on another port
#   sudo ./install.sh --help
#
# Run this from the repository, as root. It never builds: ./bin must already hold
# both binaries from ./build.sh, and their version must match src/buildinfo.
#
# An installed host is never replaced in place. When the unit exists this script
# refuses and names ./uninstall.sh, so a version change is always
#   sudo ./uninstall.sh && sudo ./install.sh
#
# Exit codes: 0 installed; 1 refused or failed; 2 usage error.
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$(readlink -f -- "${BASH_SOURCE[0]}")")" && pwd -P)"
cd -- "$ROOT"

# Deployment facts and the shared helpers live in one place, so install and
# uninstall cannot disagree about where the service lives.
PROG=install
# shellcheck source=lib/deploy.sh
. lib/deploy.sh

PREFIX="$DEFAULT_PREFIX"
BIN_DIR="$DEFAULT_BIN_DIR"
SERVICE_USER="$DEFAULT_USER"
PORT="$DEFAULT_PORT"
NO_SERVICE=0
DRY_RUN=0
STAGE=""
UNIT_TMP=""

cleanup() {
	if [ -n "${STAGE:-}" ] && [ -d "$STAGE" ]; then rm -rf -- "$STAGE"; fi
	if [ -n "${UNIT_TMP:-}" ] && [ -f "$UNIT_TMP" ]; then rm -f -- "$UNIT_TMP"; fi
}
trap cleanup EXIT
trap 'cleanup; exit 130' INT
trap 'cleanup; exit 143' TERM

usage() {
	cat <<'EOF'
usage: sudo ./install.sh [--prefix <dir>] [--bin-dir <dir>] [--port <port>]
                         [--user <name>] [--no-service] [--dry-run]

Install gassaway as a systemd system service, run from the repository.

  --prefix <dir>    program tree (default /opt/gassaway); must be absent or empty
  --bin-dir <dir>   where the gassaway symlink is created
                    (default /usr/local/bin)
  --port <port>     TCP port for the service, 1-65535 (default 4029); pass the same
                    value here and in the client's mcp.json
  --user <name>     account that runs the service (default gassaway); created as a
                    system account when missing and never removed by uninstall.sh
  --no-service      install the program and the links only; do not touch systemd
  --dry-run         check everything and print the planned commands, change nothing
  --help, -h        this text

Run ./build.sh first: this script never builds, and refuses a ./bin that is
missing or older than src/buildinfo/version.go.

The service stores its graphs in /var/lib/gassaway (gassaway:gassaway 0750),
created by systemd and preserved by uninstall.sh.
EOF
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	--prefix)
		[ "$#" -ge 2 ] && [ -n "$2" ] || usage_fail "--prefix requires a directory"
		PREFIX="$2"
		shift 2
		;;
	--bin-dir)
		[ "$#" -ge 2 ] && [ -n "$2" ] || usage_fail "--bin-dir requires a directory"
		BIN_DIR="$2"
		shift 2
		;;
	--port)
		[ "$#" -ge 2 ] && [ -n "$2" ] || usage_fail "--port requires a TCP port"
		PORT="$2"
		shift 2
		;;
	--user)
		[ "$#" -ge 2 ] && [ -n "$2" ] || usage_fail "--user requires an account name"
		SERVICE_USER="$2"
		shift 2
		;;
	--no-service) NO_SERVICE=1; shift ;;
	--dry-run) DRY_RUN=1; shift ;;
	--help|-h) usage; exit 0 ;;
	*) usage_fail "unknown argument: $1" ;;
	esac
done

check_path --prefix "$PREFIX"
check_path --bin-dir "$BIN_DIR"

case "$PORT" in
'' | *[!0-9]*) usage_fail "--port must be a TCP port between 1 and 65535, got '$PORT'" ;;
esac
# 10#$PORT keeps a leading zero decimal rather than octal.
if [ "$((10#$PORT))" -lt 1 ] || [ "$((10#$PORT))" -gt 65535 ]; then
	usage_fail "--port must be a TCP port between 1 and 65535, got '$PORT'"
fi
PORT="$((10#$PORT))"

if [[ ! "$SERVICE_USER" =~ ^[a-z_][a-z0-9_-]*$ ]]; then
	usage_fail "--user must be a lowercase account name, got '$SERVICE_USER'"
fi

[ "$(id -u)" -eq 0 ] || fail "run with sudo: this installs a system service, a unit and a system account"

command -v systemctl >/dev/null 2>&1 || fail "systemctl not found: this host does not run systemd"

# The unit file is the definition of "installed". Refusing here is what keeps a
# reinstall to exactly one path: uninstall first, then install.
if [ -e "$UNIT_PATH" ]; then
	fail "gassaway is installed ($UNIT_PATH exists); run ./uninstall.sh first"
fi

if [ -e "$PREFIX" ]; then
	[ -d "$PREFIX" ] || fail "$PREFIX exists and is not a directory"
	if [ -n "$(ls -A -- "$PREFIX")" ]; then
		if [ -f "$PREFIX/VERSION" ]; then
			fail "$PREFIX holds a partial gassaway install; run ./uninstall.sh to remove it"
		fi
		fail "$PREFIX is not a gassaway installation; refusing to touch it"
	fi
fi

[ -x bin/gassaway ] || fail "./bin/gassaway is missing; run ./build.sh first"

# The version this tree was built from. The binary agrees with it by construction,
# and after the service starts the health check proves it end to end.
SOURCE_VERSION="$(sed -n 's/^var Version = "\(.*\)"$/\1/p' src/buildinfo/version.go)"
[ -n "$SOURCE_VERSION" ] || fail "could not read the version from src/buildinfo/version.go"

# health_ok is true when the running service answers GET /healthz with 200 and
# reports the version this tree was built from. The request is one raw HTTP/1.0 GET
# over bash's /dev/tcp: the installer ships no client binary and does not depend on
# curl being installed.
health_ok() {
	local line first="" body=""
	exec 3<>"/dev/tcp/$DEFAULT_HOST/$PORT" 2>/dev/null || return 1
	printf 'GET /healthz HTTP/1.0\r\nHost: %s\r\n\r\n' "$DEFAULT_HOST" >&3 || {
		exec 3<&- 3>&-
		return 1
	}
	IFS= read -r -t 2 -u 3 line || {
		exec 3<&- 3>&-
		return 1
	}
	first="$line"
	while IFS= read -r -t 2 -u 3 line; do body+="$line"; done
	exec 3<&- 3>&-
	case "$first" in *" 200 "*) ;; *) return 1 ;; esac
	case "$body" in *"\"version\":\"$SOURCE_VERSION\""*) return 0 ;; esac
	return 1
}

# writable_parent is true when the nearest existing ancestor of a path is a
# writable directory, so a --dry-run that only prints a mkdir does not fail its
# own check.
writable_parent() {
	local dir="$1"
	while [ ! -e "$dir" ]; do dir="$(dirname -- "$dir")"; done
	[ -d "$dir" ] && [ -w "$dir" ]
}

PREFIX_PARENT="$(dirname -- "$PREFIX")"
if [ ! -d "$PREFIX_PARENT" ]; then
	writable_parent "$PREFIX_PARENT" || fail "cannot create $PREFIX_PARENT"
	run mkdir -p -- "$PREFIX_PARENT"
else
	[ -w "$PREFIX_PARENT" ] || fail "cannot write to $PREFIX_PARENT"
fi

if [ -e "$BIN_DIR" ] && [ ! -d "$BIN_DIR" ]; then
	fail "$BIN_DIR exists and is not a directory"
fi
if [ ! -d "$BIN_DIR" ]; then
	writable_parent "$BIN_DIR" || fail "cannot create $BIN_DIR"
	run mkdir -p -- "$BIN_DIR"
else
	[ -w "$BIN_DIR" ] || fail "cannot write to $BIN_DIR"
fi

# A symlink here is ours from a previous install and is replaced; anything else
# belongs to someone else and is never removed.
link="$BIN_DIR/gassaway"
if [ -e "$link" ] || [ -L "$link" ]; then
	[ -L "$link" ] || fail "$link exists and is not a symlink; refusing to replace it"
fi

if getent passwd "$SERVICE_USER" >/dev/null 2>&1; then
	if ! getent group "$SERVICE_USER" >/dev/null 2>&1; then
		command -v groupadd >/dev/null 2>&1 || fail "groupadd not found: cannot create the $SERVICE_USER group"
		run groupadd --system "$SERVICE_USER"
	fi
else
	command -v useradd >/dev/null 2>&1 || fail "useradd not found: cannot create the $SERVICE_USER account"
	run useradd --system --user-group --no-create-home --home-dir "$DATA_DIR" \
		--shell /usr/sbin/nologin --comment "gassaway memory server" "$SERVICE_USER"
fi

# A connect test is enough to see whether something already listens: the service
# binds 127.0.0.1 only, and an install that cannot bind would fail after moving
# the tree into place.
port_in_use() {
	(exec 3<>"/dev/tcp/$DEFAULT_HOST/$1") 2>/dev/null
}
if [ "$NO_SERVICE" -eq 0 ] && port_in_use "$PORT"; then
	fail "$DEFAULT_HOST:$PORT is already in use; stop what is listening there or pass --port"
fi

# Two runs must not stage into the same prefix. util-linux's flock is part of
# every systemd host.
mkdir -p -- /run/lock
exec 9>"$LOCK_PATH" || fail "cannot open $LOCK_PATH"
flock -n 9 || fail "another install.sh or uninstall.sh is already running"

if [ "$DRY_RUN" -eq 1 ]; then
	say "[dry-run] would install $PREFIX (version $SOURCE_VERSION)"
	say "[dry-run] would write $UNIT_PATH for $SERVICE_USER:$PORT"
	say "[dry-run] checks passed; nothing was changed"
	exit 0
fi

# Stage beside the target so the final mv stays on one filesystem and is atomic:
# a failure here leaves the host exactly as it was.
STAGE="$(mktemp -d -- "$PREFIX_PARENT/.gassaway-install.XXXXXX")"
mkdir -p -- "$STAGE/bin"
install -m 0755 bin/gassaway "$STAGE/bin/gassaway"
install -m 0644 README.md LICENSE THIRD_PARTY_NOTICES.md "$STAGE/"
printf '%s\n' "$SOURCE_VERSION" >"$STAGE/VERSION"
chmod 0644 -- "$STAGE/VERSION"

# The staged copy is proven by content: a copy that does not match ./bin is a
# failed copy, not a new build.
staged_sum="$(sha256sum "$STAGE/bin/gassaway")"
staged_sum="${staged_sum%% *}"
source_sum="$(sha256sum "bin/gassaway")"
source_sum="${source_sum%% *}"
[ "$staged_sum" = "$source_sum" ] || fail "staged bin/gassaway does not match the build"

UNIT_TMP="$(mktemp)"
sed -e "s|@PREFIX@|$PREFIX|g" -e "s|@USER@|$SERVICE_USER|g" -e "s|@PORT@|$PORT|g" \
	-e "s|@PROJECTS_DIR@|$PROJECTS_DIR|g" \
	"$UNIT_TEMPLATE" >"$UNIT_TMP"
if grep -qE '@(PREFIX|USER|PORT|PROJECTS_DIR)@' "$UNIT_TMP"; then
	fail "the unit template left an unsubstituted placeholder; check $UNIT_TEMPLATE"
fi

{
	for file in "${DEPLOY_FILES[@]}"; do
		printf '%s/%s\n' "$PREFIX" "$file"
	done
} | sort >"$STAGE/MANIFEST"
chmod 0644 -- "$STAGE/MANIFEST"

# mktemp creates 0700; the install root has to be traversable.
chmod 0755 -- "$STAGE"

mv -- "$STAGE" "$PREFIX"
STAGE=""
say "installed $PREFIX (version $SOURCE_VERSION)"

install -m 0644 "$UNIT_TMP" "$UNIT_PATH"
say "wrote $UNIT_PATH"

if command -v systemd-analyze >/dev/null 2>&1; then
	if ! systemd-analyze verify "$UNIT_PATH"; then
		fail "the generated unit did not verify; run ./uninstall.sh to remove the partial install"
	fi
fi

systemctl daemon-reload
systemctl reset-failed "$SERVICE" 2>/dev/null || true

ln -sfn -- "$PREFIX/bin/gassaway" "$BIN_DIR/gassaway"
say "linked $BIN_DIR/gassaway -> $PREFIX/bin/gassaway"

if [ "$NO_SERVICE" -eq 1 ]; then
	say "service not started (--no-service); start it with: systemctl enable --now $SERVICE"
else
	systemctl enable --now "$SERVICE"
	say "enabled and started $SERVICE"
fi

# Verification is the point where a wrong sandbox directive or a unit that never
# came up shows up now rather than at the first tool call. It runs against the
# installed link, not ./bin, so a broken link cannot hide.
if [ "$NO_SERVICE" -eq 0 ]; then
	for _ in $(seq 1 "$TIMEOUT_TRIES"); do
		systemctl is-active --quiet "$SERVICE" && break
		sleep "$TIMEOUT_STEP"
	done
	if ! systemctl is-active --quiet "$SERVICE"; then
		printf 'install: %s is not active; recent journal:\n' "$SERVICE" >&2
		journalctl -u "$SERVICE" -n 30 --no-pager >&2 || true
		fail "installed but not running; run ./uninstall.sh to remove it"
	fi

	ready=0
	for _ in $(seq 1 "$TIMEOUT_TRIES"); do
		if health_ok; then
			ready=1
			break
		fi
		sleep "$TIMEOUT_STEP"
	done
	if [ "$ready" -ne 1 ]; then
		fail "installed but $DEFAULT_HOST:$PORT never answered /healthz with version $SOURCE_VERSION; run ./uninstall.sh to remove it"
	fi
	say "health check passed on $DEFAULT_HOST:$PORT (version $SOURCE_VERSION)"

	target="$(readlink -f -- "$BIN_DIR/gassaway" || true)"
	case "$target" in
	"$PREFIX"/*) ;;
	*) fail "$BIN_DIR/gassaway does not resolve into $PREFIX (got '${target:-nothing}')" ;;
	esac

	state_mode="$(stat -c '%U:%G %a' -- "$DATA_DIR")"
	if [ "$state_mode" != "$SERVICE_USER:$SERVICE_USER 750" ]; then
		fail "$DATA_DIR has $state_mode, expected $SERVICE_USER:$SERVICE_USER 750"
	fi
	say "state directory $DATA_DIR is $state_mode"
fi

enabled="disabled"
[ "$NO_SERVICE" -eq 0 ] && enabled="$(systemctl is-enabled "$SERVICE" 2>/dev/null || echo unknown)"

cat <<EOF

installed
  version  $SOURCE_VERSION
  prefix   $PREFIX
  unit     $UNIT_PATH
  service  $SERVICE ($enabled)
  data     $PROJECTS_DIR ($SERVICE_USER:$SERVICE_USER 0750, kept by uninstall.sh)
  links    $BIN_DIR/gassaway
  logs     journalctl -u $SERVICE -f
EOF

if [ "$PORT" != "$DEFAULT_PORT" ]; then
	owner_home=""
	if [ -n "${SUDO_USER:-}" ]; then
		owner_home="$(getent passwd "$SUDO_USER" | cut -d: -f6)"
	fi
	warn "port $PORT is not $DEFAULT_PORT: update the client's mcp.json${owner_home:+ at $owner_home/.local/share/amanda/mcp.json} to match"
fi
