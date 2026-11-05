# Copyright (c) 2026-present Douglas Hoard. All rights reserved.
# Use of this source code is governed by the MIT license,
# which can be found in the LICENSE file.

# Shared deployment facts and helpers for install.sh and uninstall.sh. Sourced,
# never executed, and sourced from the repository rather than the installed tree,
# so uninstall still works when the tree it is removing is damaged.
#
# The caller sets PROG (install or uninstall) before sourcing this file: every
# message is prefixed with it. The caller also defines usage(); usage_fail calls it
# by name, so each script keeps its own option list.

PROG="${PROG:?set PROG before sourcing lib/deploy.sh}"

# shellcheck source=lib/defaults.sh
. "$(dirname -- "${BASH_SOURCE[0]}")/defaults.sh"

# Program tree, links, unit and state. One copy, so install and uninstall cannot
# disagree about where the service lives.
DEFAULT_PREFIX="/opt/gassaway"
DEFAULT_BIN_DIR="/usr/local/bin"
DEFAULT_USER="gassaway"
SERVICE="gassaway.service"
UNIT_PATH="/etc/systemd/system/$SERVICE"
UNIT_TEMPLATE="distribution/gassaway.service"
DATA_DIR="/var/lib/gassaway"
PROJECTS_DIR="$DATA_DIR/project"
LOCK_PATH="/run/lock/gassaway-install.lock"
TIMEOUT_TRIES=20
TIMEOUT_STEP=0.5

# Everything the install records in <prefix>/MANIFEST, in the order it is written.
# uninstall falls back to this list when a tree from before MANIFEST existed has no
# manifest of its own.
DEPLOY_FILES=(
	VERSION
	MANIFEST
	README.md
	LICENSE
	THIRD_PARTY_NOTICES.md
	bin/gassaway
)

say() { printf '%s: %s\n' "$PROG" "$*"; }
warn() { printf '%s: warning: %s\n' "$PROG" "$*" >&2; }
fail() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	exit 1
}
usage_fail() {
	printf '%s: %s\n' "$PROG" "$*" >&2
	usage >&2
	exit 2
}

# run prints the command it is about to run, and skips it under --dry-run, so a
# dry run exercises every check without changing the host.
run() {
	if [ "$DRY_RUN" -eq 1 ]; then
		printf '%s: [dry-run] %s\n' "$PROG" "$*"
		return 0
	fi
	printf '%s: %s\n' "$PROG" "$*"
	"$@"
}

# check_path rejects the shapes that make a prefix ambiguous: a relative path, a
# ~ that sudo would expand to /root, and a trailing slash.
check_path() {
	local option="$1" value="$2"
	case "$value" in
	~*) usage_fail "$option must not use ~; give an absolute path" ;;
	/*) ;;
	*) usage_fail "$option must be an absolute path" ;;
	esac
	case "$value" in
	*/) usage_fail "$option must not end with a slash" ;;
	esac
}
