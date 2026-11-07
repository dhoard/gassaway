#!/usr/bin/env bash
# Copyright (c) 2026-present Douglas Hoard
# Use of this source code is governed by the MIT license,
# which can be found in the LICENSE file.

# Remove the gassaway system service, keeping its data.
#
# This is the only operation that returns a host to "not installed", including
# from a partial state (a unit file without its program, or a program tree
# without a unit), so it is the recovery step for a failed install as well as the
# first half of every version change:
#
#   sudo ./uninstall.sh && sudo ./install.sh
#
# What it removes: the unit, the two symlinks that resolve into the prefix, and
# exactly the files the install recorded in <prefix>/MANIFEST. What it keeps:
# /var/lib/gassaway, the graphs inside it, and the gassaway account that owns
# them. Removing the account would orphan that ownership, so it is never touched
# here; --purge removes the data and leaves the account too.
#
# Run this from the repository, as root. It does not need ./bin: the repository
# may be a different version from the one installed.
#
# Exit codes: 0 removed or nothing to do; 1 refused or failed; 2 usage error.
set -euo pipefail

ROOT="$(cd -- "$(dirname -- "$(readlink -f -- "${BASH_SOURCE[0]}")")" && pwd -P)"
cd -- "$ROOT"

# Deployment facts and the shared helpers live in one place, so install and
# uninstall cannot disagree about where the service lives.
PROG=uninstall
# shellcheck source=lib/deploy.sh
. lib/deploy.sh

PREFIX="$DEFAULT_PREFIX"
BIN_DIR="$DEFAULT_BIN_DIR"
PURGE=0
KEEP_UNIT=0
DRY_RUN=0

usage() {
	cat <<'EOF'
usage: sudo ./uninstall.sh [--prefix <dir>] [--bin-dir <dir>] [--purge]
                           [--keep-unit] [--dry-run]

Remove the gassaway system service and keep its data, run from the repository.

  --prefix <dir>    program tree (default /opt/gassaway)
  --bin-dir <dir>   where the gassaway symlink lives
                    (default /usr/local/bin); pass the same value used at install
  --purge           also remove /var/lib/gassaway; refused unless the service is
                    stopped and the directory holds only project directories
  --keep-unit       leave /etc/systemd/system/gassaway.service in place (stopped
                    and disabled); the host then still counts as installed
  --dry-run         print the planned commands, change nothing
  --help, -h        this text

Kept by default: /var/lib/gassaway and the gassaway account that owns it.
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
	--purge) PURGE=1; shift ;;
	--keep-unit) KEEP_UNIT=1; shift ;;
	--dry-run) DRY_RUN=1; shift ;;
	--help|-h) usage; exit 0 ;;
	*) usage_fail "unknown argument: $1" ;;
	esac
done

check_path --prefix "$PREFIX"
check_path --bin-dir "$BIN_DIR"

[ "$(id -u)" -eq 0 ] || fail "run with sudo: this stops and removes a system service"

command -v systemctl >/dev/null 2>&1 || fail "systemctl not found: this host does not run systemd"

mkdir -p -- /run/lock
exec 9>"$LOCK_PATH" || fail "cannot open $LOCK_PATH"
flock -n 9 || fail "another install.sh or uninstall.sh is already running"

INSTALLED=0
if [ -e "$UNIT_PATH" ] || [ -e "$PREFIX" ]; then INSTALLED=1; fi

# --purge removes graphs, so it must not be the thing that stops the server: the
# caller stops it first, and "the service is not running" stays a fact this script
# observes rather than one it creates. Checking this after the stop below would
# make the guard vacuous, because the stop would already have succeeded.
if [ "$PURGE" -eq 1 ] && systemctl is-active --quiet "$SERVICE"; then
	fail "$SERVICE is running; stop it first (systemctl stop $SERVICE); no data was touched"
fi

if [ "$INSTALLED" -eq 0 ]; then
	if [ "$PURGE" -eq 0 ]; then
		say "gassaway is not installed; nothing to do"
		exit 0
	fi
	if [ ! -d "$PROJECTS_DIR" ]; then
		say "gassaway is not installed and has no data directory; nothing to do"
		exit 0
	fi
	say "gassaway is not installed; --purge will remove $PROJECTS_DIR only"
fi

# The service must be gone before its files are: removing a binary or a unit
# under a running process hides the failure instead of reporting it. A unit file
# that exists is stopped; so is a unit systemd still has loaded after its file
# was removed by hand.
if [ -e "$UNIT_PATH" ] || systemctl is-active --quiet "$SERVICE"; then
	run systemctl disable --now "$SERVICE" || true
	inactive=0
	for _ in $(seq 1 "$TIMEOUT_TRIES"); do
		if ! systemctl is-active --quiet "$SERVICE"; then
			inactive=1
			break
		fi
		sleep "$TIMEOUT_STEP"
	done
	if [ "$inactive" -ne 1 ]; then
		fail "$SERVICE is still active; stop it before removing its files"
	fi
	run systemctl reset-failed "$SERVICE" 2>/dev/null || true
fi

if [ -e "$UNIT_PATH" ]; then
	if [ "$KEEP_UNIT" -eq 1 ]; then
		warn "leaving $UNIT_PATH in place (--keep-unit); the host still counts as installed"
	else
		run rm -f -- "$UNIT_PATH"
		run systemctl daemon-reload
	fi
fi

# A link is removed only when it points into the prefix we are removing. A
# regular file or a link into somewhere else belongs to somebody else.
link="$BIN_DIR/gassaway"
if [ -L "$link" ]; then
	target="$(readlink -- "$link" || true)"
	case "$target" in
	"$PREFIX"/*)
		run rm -f -- "$link"
		say "removed $link"
		;;
	*) warn "leaving $link: it points at '${target:-nothing}', not into $PREFIX" ;;
	esac
elif [ -e "$link" ]; then
	warn "leaving $link: it is not a symlink"
fi

if [ -d "$PREFIX" ]; then
	removed=0
	skipped=0
	if [ -r "$PREFIX/MANIFEST" ]; then
		entries=()
		while IFS= read -r line || [ -n "$line" ]; do
			case "$line" in
			'' | '#'*) continue ;;
			esac
			entries+=("$line")
		done <"$PREFIX/MANIFEST"
	else
		warn "$PREFIX/MANIFEST is missing; falling back to the known file list"
		entries=()
		for file in "${DEPLOY_FILES[@]}"; do
			entries+=("$PREFIX/$file")
		done
	fi
	for entry in "${entries[@]}"; do
		case "$entry" in
		"$PREFIX"/*) ;;
		*)
			warn "skipping $entry: it is outside $PREFIX"
			skipped=$((skipped + 1))
			continue
			;;
		esac
		if [ -e "$entry" ] || [ -L "$entry" ]; then
			run rm -f -- "$entry"
			removed=$((removed + 1))
		fi
	done
	if [ "$skipped" -gt 0 ]; then
		say "removed $removed file(s) from $PREFIX, skipped $skipped"
	else
		say "removed $removed file(s) from $PREFIX"
	fi
	if [ "$DRY_RUN" -eq 0 ]; then
		find "$PREFIX" -depth -type d -empty -delete 2>/dev/null || true
	fi
	if [ -d "$PREFIX" ]; then
		warn "$PREFIX is not empty; leaving it in place:"
		ls -A -- "$PREFIX" >&2 || true
	else
		say "removed $PREFIX"
	fi
fi

if [ "$PURGE" -eq 1 ]; then
	# Already checked before the stop above; this is the second net for a unit
	# that something else started again in between.
	if systemctl is-active --quiet "$SERVICE"; then
		fail "$SERVICE is active; stop it before purging its data"
	fi
	if [ -d "$PROJECTS_DIR" ]; then
		for entry in "$PROJECTS_DIR"/*; do
			[ -e "$entry" ] || continue
			[ -d "$entry" ] || fail "unexpected file in $PROJECTS_DIR: $entry; refusing to purge"
			id="$(basename -- "$entry")"
			case "$id" in
			*[!0-9a-f]*) fail "unexpected entry in $PROJECTS_DIR: $entry; refusing to purge" ;;
			esac
			[ "${#id}" -eq 64 ] || fail "unexpected entry in $PROJECTS_DIR: $entry; refusing to purge"
			[ -f "$entry/project.json" ] || fail "$entry has no project.json; refusing to purge"
		done
		run rm -rf -- "$PROJECTS_DIR"
		say "purged $PROJECTS_DIR"
	fi
	if [ -d "$DATA_DIR" ] && [ "$DRY_RUN" -eq 0 ]; then
		rmdir -- "$DATA_DIR" 2>/dev/null || true
	fi
else
	if [ -d "$PROJECTS_DIR" ]; then
		say "kept $PROJECTS_DIR"
	else
		say "no data directory at $PROJECTS_DIR"
	fi
fi

if getent passwd gassaway >/dev/null 2>&1; then
	say "kept the gassaway account (it owns the data directory)"
fi

if [ "$KEEP_UNIT" -eq 1 ] && [ -e "$UNIT_PATH" ]; then
	warn "the unit is still installed; ./install.sh will refuse until it is removed"
fi

say "done"
