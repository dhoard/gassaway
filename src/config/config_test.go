// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for option resolution: the three required settings have no default and no
// environment fallback, a malformed value is refused instead of guessed, the data
// directory is created when it is missing, and the limits keep their variables.
package config_test

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/config"
)

// valid is a complete set of options; tests vary one field at a time.
func valid() config.Options {
	return config.Options{Host: "127.0.0.1", Port: "3001", ProjectsDir: "/srv/gassaway"}
}

// clearLimitEnv removes any limit the developer's shell may export.
func clearLimitEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{config.EnvMaxProjects, config.EnvMaxBodyBytes, config.EnvLogLevel} {
		t.Setenv(name, "")
	}
}

func TestParseAcceptsExplicitSettings(t *testing.T) {
	clearLimitEnv(t)

	cfg, err := config.Parse(config.Options{Host: "0.0.0.0", Port: "8080", ProjectsDir: "/srv/gassaway"})
	require.NoError(t, err)
	assert.Equal(t, "0.0.0.0", cfg.Host)
	assert.Equal(t, 8080, cfg.Port)
	assert.Equal(t, "/srv/gassaway", cfg.ProjectsDir)
	assert.Equal(t, config.DefaultMaxProjects, cfg.MaxProjects)
	assert.Equal(t, int64(config.DefaultMaxBodyBytes), cfg.MaxBodyBytes)
	assert.Equal(t, "0.0.0.0:8080", cfg.Addr())
}

// Each option is required: omitting it is an error that names it, so an operator
// can see which part of the command line is incomplete.
func TestEveryOptionIsRequired(t *testing.T) {
	clearLimitEnv(t)

	for _, tc := range []struct {
		name   string
		mutate func(*config.Options)
		option string
	}{
		{"no host", func(o *config.Options) { o.Host = "" }, "-host"},
		{"blank host", func(o *config.Options) { o.Host = "  " }, "-host"},
		{"no port", func(o *config.Options) { o.Port = "" }, "-port"},
		{"blank port", func(o *config.Options) { o.Port = " " }, "-port"},
		{"no data", func(o *config.Options) { o.ProjectsDir = "" }, "-project"},
		{"blank data", func(o *config.Options) { o.ProjectsDir = "\t" }, "-project"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			opts := valid()
			tc.mutate(&opts)
			_, err := config.Parse(opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.option)
			assert.Contains(t, err.Error(), "required")
		})
	}
}

func TestInvalidPortIsRefused(t *testing.T) {
	clearLimitEnv(t)

	// 0 is refused on purpose: the kernel would pick a free port, and the address
	// the service states must be the address it binds.
	for _, value := range []string{"0", "-1", "65536", "70000", "abc", "30.5", "3e3"} {
		t.Run(value, func(t *testing.T) {
			opts := valid()
			opts.Port = value
			_, err := config.Parse(opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "-port")
			assert.Contains(t, err.Error(), "1 and 65535")
		})
	}
}

// The retired variables must not revive a default host, port or data directory.
func TestEnvironmentSuppliesNoDeploymentSetting(t *testing.T) {
	clearLimitEnv(t)
	t.Setenv("GASSAWAY_BIND", "0.0.0.0")
	t.Setenv("GASSAWAY_PORT", "4000")
	t.Setenv("GASSAWAY_PROJECTS_DIR", "/srv/hidden")
	t.Setenv("XDG_DATA_HOME", "/xdg")
	t.Setenv("HOME", t.TempDir())

	_, err := config.Parse(config.Options{})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "-host")

	// With the flags given, the flags win and the environment is irrelevant.
	cfg, err := config.Parse(valid())
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1", cfg.Host)
	assert.Equal(t, 3001, cfg.Port)
	assert.Equal(t, "/srv/gassaway", cfg.ProjectsDir)
}

func TestLimitsComeFromEnvironment(t *testing.T) {
	clearLimitEnv(t)
	t.Setenv(config.EnvMaxProjects, "7")
	t.Setenv(config.EnvMaxBodyBytes, "1024")

	cfg, err := config.Parse(valid())
	require.NoError(t, err)
	assert.Equal(t, 7, cfg.MaxProjects)
	assert.Equal(t, int64(1024), cfg.MaxBodyBytes)
}

func TestInvalidLimitsAreRefused(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{config.EnvMaxProjects, "0"},
		{config.EnvMaxProjects, "many"},
		{config.EnvMaxBodyBytes, "0"},
		{config.EnvMaxBodyBytes, "-5"},
	} {
		t.Run(tc.name+"="+tc.value, func(t *testing.T) {
			clearLimitEnv(t)
			t.Setenv(tc.name, tc.value)
			_, err := config.Parse(valid())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.name)
		})
	}
}

func TestEnsureProjectDirCreatesMissingParents(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nested", "deeper", "projects")
	cfg := config.Config{ProjectsDir: dir}

	require.NoError(t, cfg.EnsureProjectDir())
	info, err := os.Stat(dir)
	require.NoError(t, err)
	assert.True(t, info.IsDir())

	// Calling it again is how every restart looks: it must be a no-op.
	require.NoError(t, cfg.EnsureProjectDir())
}

func TestEnsureProjectDirLeavesExistingDirectoryAlone(t *testing.T) {
	dir := t.TempDir()
	graph := filepath.Join(dir, "alpha", "memory.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(graph), 0o755))
	require.NoError(t, os.WriteFile(graph, []byte("{}\n"), 0o644))

	require.NoError(t, config.Config{ProjectsDir: dir}.EnsureProjectDir())

	content, err := os.ReadFile(graph)
	require.NoError(t, err)
	assert.Equal(t, "{}\n", string(content))
}

func TestEnsureProjectDirFailsOnUnusablePath(t *testing.T) {
	base := t.TempDir()
	file := filepath.Join(base, "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o644))

	for _, tc := range []struct{ name, dir string }{
		{"path is a file", file},
		{"parent is a file", filepath.Join(file, "projects")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := config.Config{ProjectsDir: tc.dir}.EnsureProjectDir()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.dir)
		})
	}
}

// The listener address is formed with net.JoinHostPort, so an IPv6 literal works.
func TestAddr(t *testing.T) {
	for _, tc := range []struct {
		host string
		port int
		want string
	}{
		{"127.0.0.1", 3001, "127.0.0.1:3001"},
		{"localhost", 3001, "localhost:3001"},
		{"::1", 3001, "[::1]:3001"},
		{"0.0.0.0", 80, "0.0.0.0:80"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			cfg := config.Config{Host: tc.host, Port: tc.port}
			assert.Equal(t, tc.want, cfg.Addr())
		})
	}
}

// GASSAWAY_LOG_LEVEL is a tuning knob with a validated value: a service that
// silently ignores the level it was told to use is harder to diagnose than one
// that refuses to start.
func TestLogLevelIsValidated(t *testing.T) {
	clearLimitEnv(t)
	t.Setenv(config.EnvLogLevel, "")
	cfg, err := config.Parse(valid())
	require.NoError(t, err)
	assert.Equal(t, config.DefaultLogLevel, cfg.LogLevel, "info is the default")

	for value, want := range map[string]slog.Level{
		"debug":   slog.LevelDebug,
		"info":    slog.LevelInfo,
		"warning": slog.LevelWarn,
		"warn":    slog.LevelWarn,
		"ERROR":   slog.LevelError,
	} {
		t.Setenv(config.EnvLogLevel, value)
		cfg, err := config.Parse(valid())
		require.NoError(t, err, "value %q", value)
		assert.Equal(t, want, cfg.LogLevel, "value %q", value)
	}

	for _, value := range []string{"trace", "verbose", "1", " "} {
		t.Setenv(config.EnvLogLevel, value)
		_, err := config.Parse(valid())
		require.Error(t, err, "value %q must be refused", value)
		assert.Contains(t, err.Error(), config.EnvLogLevel)
		assert.Contains(t, err.Error(), value)
	}
}
