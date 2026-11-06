// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for what an operator sees. The property under test is not the wording
// but the shape: exactly one record per failed call, carrying the project, the
// tool and the reason, and no record for a success at the default level. The
// sentinel test is the privacy guard: graph content must never reach a log.
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/server"
)

// syncBuffer collects log lines written from several goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// loggedServer is a server whose log records the test can read.
func loggedServer(t *testing.T, level slog.Level) (*httptest.Server, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	cfg := config.Config{
		Host:         "127.0.0.1",
		ProjectsDir:  t.TempDir(),
		MaxProjects:  10,
		MaxBodyBytes: 1 << 20,
	}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level})))
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return ts, logs
}

func records(logs string, level string) []string {
	var out []string
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "level="+level) {
			out = append(out, line)
		}
	}
	return out
}

func TestFailedToolCallIsOneWarnRecord(t *testing.T) {
	ts, logs := loggedServer(t, slog.LevelInfo)
	cs := connect(t, ts.URL+"/project/"+idA)

	// A missing entity is a tool execution error: the model can correct it.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "add_observations",
		Arguments: map[string]any{"observations": []map[string]any{{"entityName": "nobody", "contents": []string{"x"}}}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	warns := records(logs.String(), "WARN")
	require.Len(t, warns, 1, "a failed call must produce exactly one record; log was:\n%s", logs.String())
	assert.Contains(t, warns[0], "tool call failed")
	assert.Contains(t, warns[0], "project="+idA)
	assert.Contains(t, warns[0], "tool=add_observations")
	assert.Contains(t, warns[0], "error=")
	assert.Contains(t, warns[0], "duration_ms=")
}

// Argument validation never reaches a handler, so it is the case a
// handler-level instrumentation would miss.
func TestInvalidArgumentsAreLogged(t *testing.T) {
	ts, logs := loggedServer(t, slog.LevelInfo)
	cs := connect(t, ts.URL+"/project/"+idA)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "add_observations",
		Arguments: map[string]any{"entityName": "Ada", "contents": []string{"x"}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	warns := records(logs.String(), "WARN")
	require.Len(t, warns, 1, "log was:\n%s", logs.String())
	assert.Contains(t, warns[0], "tool=add_observations")
	assert.Contains(t, warns[0], "observations", "the reason the call failed is the diagnostic")
}

func TestSuccessfulCallsAreNotWarned(t *testing.T) {
	ts, logs := loggedServer(t, slog.LevelInfo)
	cs := connect(t, ts.URL+"/project/"+idA)
	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{}}},
	})
	callTool(t, cs, "read_graph", map[string]any{})

	assert.Empty(t, records(logs.String(), "WARN"), "log was:\n%s", logs.String())
	assert.Empty(t, records(logs.String(), "ERROR"), "log was:\n%s", logs.String())

	// At debug the successes become visible, still without any graph content.
	tsDebug, debugLogs := loggedServer(t, slog.LevelDebug)
	callTool(t, connect(t, tsDebug.URL+"/project/"+idA), "read_graph", map[string]any{})
	assert.NotEmpty(t, records(debugLogs.String(), "DEBUG"))
}

// Graph content must never reach a log record, at any level.
func TestLogsCarryNoGraphContent(t *testing.T) {
	const sentinel = "sentinel-observation-1b7f"
	for _, level := range []slog.Level{slog.LevelDebug, slog.LevelInfo} {
		ts, logs := loggedServer(t, level)
		cs := connect(t, ts.URL+"/project/"+idA)
		callTool(t, cs, "create_entities", map[string]any{
			"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{sentinel}}},
		})
		// A failure whose error text does not quote the observation.
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "add_observations",
			Arguments: map[string]any{"observations": []map[string]any{{"entityName": "nobody", "contents": []string{sentinel}}}},
		})
		require.NoError(t, err)
		require.True(t, res.IsError)

		assert.NotContains(t, logs.String(), sentinel, "level %v: an observation reached the log", level)
	}
}

// The HTTP edge records what it refuses, with the method and status, and still
// answers with a message the client can act on.
func TestEdgeRefusalsAreLogged(t *testing.T) {
	ts, logs := loggedServer(t, slog.LevelInfo)

	for _, tc := range []struct{ method, path, session string }{
		{http.MethodPost, "/project/not-a-hash", ""},
		{http.MethodGet, "/nope", ""},
		{http.MethodPost, "/project/" + idB, idA + ".SESSION"},
	} {
		req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(initializeBare))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if tc.session != "" {
			req.Header.Set("Mcp-Session-Id", tc.session)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}

	warns := records(logs.String(), "WARN")
	require.Len(t, warns, 3, "log was:\n%s", logs.String())
	for _, want := range []string{"status=400", "status=404", "invalid project ID", "no route for", "session not found"} {
		assert.Contains(t, logs.String(), want)
	}
	for _, record := range warns {
		assert.Contains(t, record, "method=", "a refusal names the method")
	}
}

// A 5xx is answered generically: the detail goes to the log, not to the client.
func TestServerErrorBodyIsGeneric(t *testing.T) {
	root := t.TempDir()
	logs := &syncBuffer{}
	cfg := config.Config{Host: "127.0.0.1", ProjectsDir: root, MaxProjects: 10, MaxBodyBytes: 1 << 20}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(logs, nil)))
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)

	// A project directory that exists but whose metadata is corrupt: opening it
	// fails, and the failure names a path the client has no business seeing.
	require.NoError(t, os.MkdirAll(filepath.Join(root, idA), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, idA, "project.json"), []byte("not json"), 0o600))

	resp := post(t, ts.URL+"/project/"+idA, initializeBare)
	defer resp.Body.Close()
	require.Equal(t, http.StatusInternalServerError, resp.StatusCode)

	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	assert.Equal(t, "project storage is not available", payload["error"])
	assert.NotContains(t, payload["error"], root, "the client is not told where the data lives")

	assert.Contains(t, logs.String(), "project metadata does not match its ID")
	assert.Contains(t, logs.String(), "status=500")
}
