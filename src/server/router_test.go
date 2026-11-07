// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for the HTTP edge: the one project URL, the refusals that must stay
// loud, the health endpoint, and the limits.
package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/graph"
	"github.com/dhoard/gassaway/src/server"
)

const idA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const idB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func newServer(t *testing.T, maxProjects int) (*server.Server, *httptest.Server, config.Config) {
	t.Helper()
	cfg := config.Config{
		Host:         "127.0.0.1",
		ProjectsDir:  t.TempDir(),
		MaxProjects:  maxProjects,
		MaxBodyBytes: 1 << 20,
	}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return svc, ts, cfg
}

func connect(t *testing.T, endpoint string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "router-test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:             endpoint,
		DisableStandaloneSSE: true,
	}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool %s failed", name)
	encoded, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}

// initializeBare is the initialize POST used where the request is refused before the
// body is read: the project has to resolve first, so the params do not matter.
const initializeBare = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`

// initialize is the initialize POST a streamable MCP client sends to open a session.
const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`

func TestHealthReportsTheService(t *testing.T) {
	_, ts, _ := newServer(t, 10)

	payload := getJSON(t, ts.URL+"/healthz")
	assert.Equal(t, "ok", payload["status"])
	assert.Equal(t, buildinfo.Version, payload["version"])
	assert.Equal(t, float64(0), payload["projects"], "an untouched data directory stores nothing")

	// A project that only reads leaves nothing on disk, so it is not counted; the
	// count is the operator view of what the data directory actually holds.
	cs := connect(t, ts.URL+"/project/"+idA)
	callTool(t, cs, "read_graph", map[string]any{})
	assert.Equal(t, float64(0), getJSON(t, ts.URL+"/healthz")["projects"], "a project with no stored data is not counted")

	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{}}},
	})
	assert.Equal(t, float64(1), getJSON(t, ts.URL+"/healthz")["projects"])
}

func TestProjectRoundTrip(t *testing.T) {
	_, ts, cfg := newServer(t, 10)
	cs := connect(t, ts.URL+"/project/"+idA)

	empty := callTool(t, cs, "read_graph", map[string]any{})
	assert.Empty(t, empty["entities"])
	assert.Empty(t, empty["relations"])

	created := callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{"writes Go"}}},
	})
	require.Len(t, created["entities"], 1)

	graph := callTool(t, cs, "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1)

	onDisk := filepath.Join(cfg.ProjectsDir, idA, "memory.jsonl")
	_, err := os.Stat(onDisk)
	require.NoError(t, err)
}

// The ID is used unchanged and lands in the metadata exactly as supplied, at the
// moment the project first holds something.
func TestPathIDIsUsedUnchanged(t *testing.T) {
	_, ts, cfg := newServer(t, 10)
	cs := connect(t, ts.URL+"/project/"+idA)
	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{}}},
	})

	data, err := os.ReadFile(filepath.Join(cfg.ProjectsDir, idA, "project.json"))
	require.NoError(t, err)
	var metadata map[string]any
	require.NoError(t, json.Unmarshal(data, &metadata))
	assert.Equal(t, idA, metadata["projectId"])
	assert.Equal(t, float64(1), metadata["schemaVersion"])
}

func TestInvalidIdentityLeavesNoProject(t *testing.T) {
	_, ts, cfg := newServer(t, 10)
	for _, id := range []string{"app", "..%2Fapp", "abc", strings.Repeat("A", 64), strings.Repeat("g", 64)} {
		resp := post(t, ts.URL+"/project/"+id, initializeBare)
		assert.Equal(t, http.StatusBadRequest, resp.StatusCode, "id %q must be refused", id)
		resp.Body.Close()
	}
	entries, err := os.ReadDir(cfg.ProjectsDir)
	require.NoError(t, err)
	assert.Empty(t, entries)
}

// Everything that is not one of the two URLs is a 404 naming them, so an old
// address fails loudly on the first request instead of looking like a project.
func TestOnlyTwoURLsExist(t *testing.T) {
	_, ts, _ := newServer(t, 10)
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/projects"},
		{http.MethodPost, "/projects/" + idA + "/mcp"},
		{http.MethodPost, "/project"},
		{http.MethodPost, "/project/"},
		{http.MethodGet, "/nope"},
	} {
		req, err := http.NewRequest(tc.method, ts.URL+tc.path, strings.NewReader(initializeBare))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode, "%s %s", tc.method, tc.path)
		assert.Contains(t, string(body), "/project/<id>", "%s %s", tc.method, tc.path)
		assert.Contains(t, string(body), "/healthz", "%s %s", tc.method, tc.path)
	}
}

func TestBadIDInPathIsRefused(t *testing.T) {
	_, ts, _ := newServer(t, 10)
	resp := post(t, ts.URL+"/project/Bad%20Name", initializeBare)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "invalid project ID")
}

// The cap bounds how many project runtimes are held at once; reaching it drops
// the least recently used runtime instead of refusing the new project. A dropped
// runtime is rebuilt on demand, its data is untouched, and a session that still
// holds the dropped server keeps working because the graph's write lock is shared
// by path rather than owned by a Store.
func TestProjectCapEvictsTheLeastRecentlyUsed(t *testing.T) {
	logs := &bytes.Buffer{}
	cfg := config.Config{
		Host:         "127.0.0.1",
		ProjectsDir:  t.TempDir(),
		MaxProjects:  1,
		MaxBodyBytes: 1 << 20,
	}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)

	write := func(id, name string) *mcp.ClientSession {
		cs := connect(t, ts.URL+"/project/"+id)
		callTool(t, cs, "create_entities", map[string]any{
			"entities": []map[string]any{{"name": name, "entityType": "person", "observations": []string{}}},
		})
		return cs
	}

	sessionA := write(idA, "Ada")
	write(idB, "Grace") // evicts A's runtime

	assert.Contains(t, logs.String(), "evicted project runtime", "the cap drops a runtime rather than refusing a project")

	// The session that holds the evicted runtime still reads and writes A's graph.
	graph := callTool(t, sessionA, "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1, "an evicted runtime keeps serving the session that holds it")
	assert.Equal(t, "Ada", graph["entities"].([]any)[0].(map[string]any)["name"])

	// A fresh session against A rebuilds the runtime from the file, and writes
	// from the old and new runtime are serialized by the shared path lock.
	sessionA2 := connect(t, ts.URL+"/project/"+idA)
	var wg sync.WaitGroup
	for _, w := range []struct {
		cs   *mcp.ClientSession
		name string
	}{{sessionA, "Ada2"}, {sessionA2, "Ada3"}} {
		wg.Add(1)
		go func(cs *mcp.ClientSession, name string) {
			defer wg.Done()
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
				Name:      "create_entities",
				Arguments: map[string]any{"entities": []map[string]any{{"name": name, "entityType": "person", "observations": []string{}}}},
			})
			assert.NoError(t, err)
			assert.False(t, res.IsError)
		}(w.cs, w.name)
	}
	wg.Wait()

	final := callTool(t, sessionA2, "read_graph", map[string]any{})
	assert.Len(t, final["entities"], 3, "no write may be lost between two runtimes over one graph")
}

func TestTraversalPathIsCleanedNotServed(t *testing.T) {
	svc, _, _ := newServer(t, 10)

	req := httptest.NewRequest(http.MethodGet, "/project/../etc", nil)
	req.URL = &url.URL{Path: "/project/../etc"}
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)

	assert.Equal(t, http.StatusTemporaryRedirect, rec.Code, "ServeMux cleans a traversal path")
	assert.Equal(t, "/etc", rec.Header().Get("Location"), "the cleaned path names no project")
}

func TestResourceIsReadable(t *testing.T) {
	_, ts, _ := newServer(t, 10)
	cs := connect(t, ts.URL+"/project/"+idA)
	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{}}},
	})

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "memory://knowledge-graph"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Contains(t, res.Contents[0].Text, "Ada")
}

// getJSON fetches a body that is a JSON object.
func getJSON(t *testing.T, endpoint string) map[string]any {
	t.Helper()
	resp, err := http.Get(endpoint)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	var payload map[string]any
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&payload))
	return payload
}

// post sends an MCP POST with the headers the streamable transport requires.
func post(t *testing.T, endpoint, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewBufferString(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	return resp
}

func TestMetadataSurvivesRestartAndRejectsCorruption(t *testing.T) {
	root := t.TempDir()
	registry := server.NewRegistry(root, 10, nil)
	project, err := registry.Get(idA)
	require.NoError(t, err)
	// Reading creates nothing; the first write publishes the metadata.
	_, err = project.Store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)
	file := filepath.Join(root, idA, "project.json")
	original, err := os.ReadFile(file)
	require.NoError(t, err)
	_, err = server.NewRegistry(root, 10, nil).Get(idA)
	require.NoError(t, err)
	after, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, original, after)
	for _, bad := range []string{"{}", "not json", `{"schemaVersion":2,"projectId":"` + idA + `"}`, `{"schemaVersion":1,"projectId":"` + idB + `"}`} {
		require.NoError(t, os.WriteFile(file, []byte(bad), 0o600))
		_, err := server.NewRegistry(root, 10, nil).Get(idA)
		require.Error(t, err)
		contents, err := os.ReadFile(file)
		require.NoError(t, err)
		assert.Equal(t, bad, string(contents))
	}
}

func TestLegacyDirectoriesAreIgnored(t *testing.T) {
	root := t.TempDir()
	legacy := filepath.Join(root, "app")
	require.NoError(t, os.Mkdir(legacy, 0o755))
	file := filepath.Join(legacy, "memory.jsonl")
	require.NoError(t, os.WriteFile(file, []byte("old graph"), 0o600))
	registry := server.NewRegistry(root, 10, nil)
	project, err := registry.Get(idA)
	require.NoError(t, err)
	_, err = project.Store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)
	count, err := registry.Count()
	require.NoError(t, err)
	assert.Equal(t, 1, count, "a basename directory is not a project")
	contents, err := os.ReadFile(file)
	require.NoError(t, err)
	assert.Equal(t, "old graph", string(contents))
}

// A connected Streamable HTTP client keeps a standalone SSE stream open, which
// never goes idle. Shutdown must not report failure because of it: every
// acknowledged write is durable, so draining and then closing is the contract.
func TestShutdownSucceedsWithAConnectedClient(t *testing.T) {
	cfg := config.Config{
		Host:         "127.0.0.1",
		ProjectsDir:  t.TempDir(),
		MaxProjects:  10,
		MaxBodyBytes: 1 << 20,
	}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	serveErr := make(chan error, 1)
	go func() { serveErr <- svc.Serve(ln) }()
	base := "http://" + ln.Addr().String()

	// The default client transport: standalone SSE stays enabled, which is what a
	// URL-only client such as a harness uses.
	client := mcp.NewClient(&mcp.Implementation{Name: "shutdown-test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: base + "/project/" + idA}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{"durable"}}},
	})

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	start := time.Now()
	require.NoError(t, svc.Shutdown(ctx), "shutdown must succeed once the grace period is spent")
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 5*time.Second, "shutdown must not wait indefinitely")

	select {
	case err := <-serveErr:
		assert.NoError(t, err, "Serve reports a clean stop")
	case <-time.After(5 * time.Second):
		t.Fatal("Serve did not return after shutdown")
	}

	// The acknowledged write is on disk, so the next process sees it.
	graph, err := os.ReadFile(filepath.Join(cfg.ProjectsDir, idA, "memory.jsonl"))
	require.NoError(t, err)
	assert.Contains(t, string(graph), "durable")
}
