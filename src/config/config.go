// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Package config resolves the server's settings.
//
// Three of them — the host to bind, the port, and the data directory — are
// deployment facts, so they come only from the command line and have no default:
// a service that quietly invents an address or a directory is hard to find on a
// host that runs more than one of them. Nothing else may supply them either, so
// an environment variable cannot resurrect a default behind the operator's back.
// The limits are tuning knobs rather than deployment identity, and keep their
// variables and defaults.
//
// Parsing is strict — a malformed port is an error naming the option, not a
// silent fallback — because a server that ignores what it was told is harder to
// diagnose than one that refuses to start.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"time"
)

// Defaults for the settings a deployment does not normally restate.
const (
	DefaultMaxProjects  = 1024
	DefaultMaxBodyBytes = 4 << 20
	// DefaultSessionTimeout is how long a session may go without a request before
	// the server closes it. It is long on purpose: a harness can sit through
	// hours of work between two memory calls, and the limit exists to reclaim the
	// sessions of clients that died without ending them, not to police live ones.
	DefaultSessionTimeout = 24 * time.Hour
	// DefaultBodyReadTimeout bounds how long a client may take to deliver a
	// request body once its headers have arrived.
	DefaultBodyReadTimeout = 30 * time.Second
)

// Variables the server reads for its limits. The three deployment settings have
// no variable: they are flags only.
const (
	EnvMaxProjects    = "GASSAWAY_MAX_PROJECTS"
	EnvMaxBodyBytes   = "GASSAWAY_MAX_BODY_BYTES"
	EnvSessionTimeout = "GASSAWAY_SESSION_TIMEOUT"
	EnvLogLevel       = "GASSAWAY_LOG_LEVEL"
)

// Accepted ports. 0 is refused even though the kernel would assign a free port:
// the address a service states is the address it binds.
const (
	MinPort = 1
	MaxPort = 65535
)

// dirMode is the permission new data and project directories are created with.
const dirMode os.FileMode = 0o755

// Options are the settings the command line supplies. All three are required and
// none has a default.
type Options struct {
	// Host is the hostname or IP address to bind.
	Host string
	// Port is the TCP port as typed, so that a missing value and a valid one are
	// distinguishable and a malformed one can be quoted back to the operator.
	Port string
	// ProjectsDir is the directory holding one subdirectory per project.
	ProjectsDir string
}

// DefaultLogLevel is the level used when GASSAWAY_LOG_LEVEL is unset: failures are
// visible, successes are not.
const DefaultLogLevel = slog.LevelInfo

// Config is the resolved server configuration.
type Config struct {
	// Host is the address the listener binds to.
	Host string
	// Port is the TCP port; 0 is not accepted by Parse.
	Port int
	// ProjectsDir is the directory holding one subdirectory per project.
	ProjectsDir string
	// MaxProjects bounds how many projects may be opened in one process.
	MaxProjects int
	// MaxBodyBytes bounds a request body.
	MaxBodyBytes int64
	// SessionTimeout closes a session that has received no request for this
	// long. Zero never closes an idle session.
	SessionTimeout time.Duration
	// BodyReadTimeout bounds the reading of one request body. Zero is no bound.
	BodyReadTimeout time.Duration
	// LogLevel is the minimum level the server logs at.
	LogLevel slog.Level
}

// Parse validates the required options and resolves the remaining settings.
// Host and ProjectsDir are trimmed before use, so a value that is only whitespace is
// rejected rather than turned into a directory named " ". Every error names the
// option that has to be fixed.
func Parse(opts Options) (Config, error) {
	host := strings.TrimSpace(opts.Host)
	if host == "" {
		return Config{}, errors.New("--host is required: the hostname or IP address to bind")
	}

	rawPort := strings.TrimSpace(opts.Port)
	if rawPort == "" {
		return Config{}, fmt.Errorf("--port is required: a TCP port between %d and %d", MinPort, MaxPort)
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < MinPort || port > MaxPort {
		return Config{}, fmt.Errorf("--port must be a TCP port between %d and %d, got %q", MinPort, MaxPort, opts.Port)
	}

	dir := strings.TrimSpace(opts.ProjectsDir)
	if dir == "" {
		return Config{}, errors.New("--project is required: the directory that holds the projects")
	}

	cfg := Config{
		Host:         host,
		Port:         port,
		ProjectsDir:  dir,
		MaxProjects:  DefaultMaxProjects,
		MaxBodyBytes: DefaultMaxBodyBytes,
		LogLevel:     DefaultLogLevel,

		SessionTimeout:  DefaultSessionTimeout,
		BodyReadTimeout: DefaultBodyReadTimeout,
	}

	if v := os.Getenv(EnvMaxProjects); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer, got %q", EnvMaxProjects, v)
		}
		cfg.MaxProjects = n
	}
	if v := os.Getenv(EnvLogLevel); v != "" {
		level, err := ParseLogLevel(v)
		if err != nil {
			return Config{}, err
		}
		cfg.LogLevel = level
	}
	if v := os.Getenv(EnvMaxBodyBytes); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 1 {
			return Config{}, fmt.Errorf("%s must be a positive integer, got %q", EnvMaxBodyBytes, v)
		}
		cfg.MaxBodyBytes = n
	}
	if v := os.Getenv(EnvSessionTimeout); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return Config{}, fmt.Errorf("%s must be a positive duration such as 24h or 90m, got %q", EnvSessionTimeout, v)
		}
		cfg.SessionTimeout = d
	}
	return cfg, nil
}

// ParseLogLevel turns a GASSAWAY_LOG_LEVEL value into a level. An unknown value
// is refused rather than defaulted: a service that silently ignores the level it
// was told to use is harder to diagnose than one that will not start.
func ParseLogLevel(value string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn", "warning":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	}
	return 0, fmt.Errorf("%s must be one of debug, info, warn, error, got %q", EnvLogLevel, value)
}

// EnsureProjectDir creates the data directory, and any missing parent, when it does
// not exist yet. An existing directory is left exactly as it is. The server calls
// this before it opens its listener, so a directory that cannot be used fails the
// start rather than the first request.
func (c Config) EnsureProjectDir() error {
	if err := os.MkdirAll(c.ProjectsDir, dirMode); err != nil {
		return fmt.Errorf("creating data directory %s: %w", c.ProjectsDir, err)
	}
	return nil
}

// Addr is the host:port the listener binds. net.JoinHostPort brackets IPv6
// literals, so -host ::1 is a usable address and not a malformed "::1:3001".
func (c Config) Addr() string {
	return net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
}
