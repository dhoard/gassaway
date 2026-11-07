// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Package buildinfo carries the version string stamped into the server binary.
//
// The value is a var so a release build can override it without editing source:
// go build -ldflags "-X github.com/dhoard/gassaway/src/buildinfo.Version=0.1.0".
// build.sh passes those ldflags, and the value is reported by the /healthz payload
// and the start-up log line.
package buildinfo

// Version is the project version: the single source of truth for the /healthz
// payload and the start-up log line.
var Version = "0.1.0"
