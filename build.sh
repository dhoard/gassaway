#!/usr/bin/env bash
# Copyright (c) 2026-present Douglas Hoard. All rights reserved.
# Use of this source code is governed by the MIT license,
# which can be found in the LICENSE file.

# Build gate and binaries for gassaway.
#
# The gate is what CI on a developer machine would run: formatting, vet, the whole
# test tree under the race detector, then both binaries. It fails on the first
# problem and leaves nothing half-built: the binaries are written only after every
# check has passed, so a green `bin/` always means a green tree.
#
# Usage:
#   ./build.sh              format check, vet, test -race, build the binary
#   ./build.sh --no-tests   the same without running the tests (work in progress)
#   ./build.sh --help
#
# Output: bin/gassaway (the server) — statically linked, so it runs on a host with
# no Go toolchain and no runtime installed.
set -euo pipefail

cd "$(dirname -- "${BASH_SOURCE[0]}")"

usage() {
	cat <<'EOF'
Usage: ./build.sh [--no-tests] [--help]

  (default)   gofmt check, go vet, go test -race ./..., then build bin/gassaway
  --no-tests  the same without running the tests
  --help      this text
EOF
}

SKIP_TESTS=0
for arg in "$@"; do
	case "$arg" in
	--no-tests) SKIP_TESTS=1 ;;
	-h | --help)
		usage
		exit 0
		;;
	*)
		echo "build: unknown option: $arg" >&2
		usage >&2
		exit 2
		;;
	esac
done

fail() {
	echo "build: $*" >&2
	exit 1
}

# The version lives in one place; stamp it into the binary so /healthz and the
# start-up log line agree, and so a release can bump it without touching code.
VERSION=$(sed -n 's/^var Version = "\(.*\)"$/\1/p' src/buildinfo/version.go)
[ -n "$VERSION" ] || fail "could not read the version from src/buildinfo/version.go"
LDFLAGS="-s -w -X github.com/dhoard/gassaway/src/buildinfo.Version=${VERSION}"

echo "build: gofmt"
UNFORMATTED=$(gofmt -l ./src)
[ -z "$UNFORMATTED" ] || fail "not gofmt'd:
${UNFORMATTED}"

echo "build: go vet"
go vet ./...

if [ "$SKIP_TESTS" -eq 0 ]; then
	echo "build: go test -race ./..."
	go test -race ./...
else
	echo "build: tests skipped (--no-tests)"
fi

mkdir -p bin
echo "build: binaries"
CGO_ENABLED=0 go build -trimpath -ldflags "$LDFLAGS" -o bin/gassaway ./src/cmd/gassaway

echo "build: ok — bin/gassaway (server), version ${VERSION}"
