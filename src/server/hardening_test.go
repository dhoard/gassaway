// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for the properties that keep one long-lived process healthy for every
// project it serves: what may reach a log, what may be stored, what a lost
// directory or a dead client leaves behind, and how long a request may stall.
package server_test

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/server"
)

// tunedServer is a server whose configuration the test adjusts and whose log
// records it can read.
func tunedServer(t *testing.T, tune func(*config.Config)) (*httptest.Server, *syncBuffer, config.Config) {
	t.Helper()
	logs := &syncBuffer{}
	cfg := config.Config{
		Host:         "127.0.0.1",
		ProjectsDir:  t.TempDir(),
		MaxProjects:  10,
		MaxBodyBytes: 1 << 20,
	}
	if tune != nil {
		tune(&cfg)
	}
	svc := server.New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	ts := httptest.NewServer(svc.Handler())
	t.Cleanup(ts.Close)
	return ts, logs, cfg
}

// A schema validator quotes the value it rejected. When the value is a whole
// batch of entities, the error string is the batch; the log must carry where the
// arguments failed and which rule they broke, and none of the value.
func TestRejectedArgumentsAreNotLogged(t *testing.T) {
	const sentinel = "sentinel-rejected-value-93c1"
	ts, logs, _ := tunedServer(t, nil)
	cs := connect(t, ts.URL+"/project/"+idA)

	// The array sent as a string: a mistake a model makes, and the case that put
	// entity names and observations into the journal.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_entities",
		Arguments: map[string]any{
			"entities": `[{"name": "` + sentinel + `", "entityType": "note: has type \"x\", want \"y\"", "observations": ["` + sentinel + `"]}]`,
		},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)

	assert.NotContains(t, logs.String(), sentinel, "the rejected value reached the log")
	warns := records(logs.String(), "WARN")
	require.Len(t, warns, 1, "log was:\n%s", logs.String())
	assert.Contains(t, warns[0], "tool=create_entities")
	assert.Contains(t, warns[0], "invalid arguments at /properties/entities: type")
	assert.Contains(t, warns[0], `want \"array\"`, "the expected type is the diagnostic")
	assert.NotContains(t, warns[0], `want \"y\"`, "text inside the value must not pass for the diagnostic")
}

// "entity X not found" names X. The model is told; the log is not.
func TestMissingEntityNameIsNotLogged(t *testing.T) {
	const sentinel = "sentinel-entity-name-5d20"
	ts, logs, _ := tunedServer(t, nil)
	cs := connect(t, ts.URL+"/project/"+idA)

	for _, call := range []struct {
		tool string
		args map[string]any
	}{
		{"add_observations", map[string]any{"observations": []map[string]any{{"entityName": sentinel, "contents": []string{"x"}}}}},
		{"create_relations", map[string]any{"relations": []map[string]any{{"from": sentinel, "to": sentinel, "relationType": "knows"}}}},
	} {
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: call.tool, Arguments: call.args})
		require.NoError(t, err)
		require.True(t, res.IsError)
		text := res.Content[0].(*mcp.TextContent).Text
		assert.Contains(t, text, sentinel, "%s: the model must be told which name was missing", call.tool)
	}

	assert.NotContains(t, logs.String(), sentinel, "an entity name reached the log")
	assert.Len(t, records(logs.String(), "WARN"), 2)
	assert.Contains(t, logs.String(), `error="entity not found"`)
}

// An entity with an empty name used to be acknowledged, written, and then never
// returned by any tool. It is refused, the model is told why, and nothing is
// stored — not even the project directory.
func TestEmptyNamesAreRefusedAndNothingIsStored(t *testing.T) {
	ts, _, cfg := tunedServer(t, nil)
	cs := connect(t, ts.URL+"/project/"+idA)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_entities",
		Arguments: map[string]any{"entities": []map[string]any{
			{"name": "Ada", "entityType": "person", "observations": []string{}},
			{"name": "", "entityType": "person", "observations": []string{"lost"}},
		}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, res.Content[0].(*mcp.TextContent).Text, "entity at index 1 has an empty name")

	_, statErr := os.Stat(filepath.Join(cfg.ProjectsDir, idA))
	assert.True(t, os.IsNotExist(statErr), "a refused call must not create the project")

	graph := callTool(t, cs, "read_graph", map[string]any{})
	assert.Empty(t, graph["entities"])
}

// An operator who removes a project's directory while the service is running
// must not leave that project unable to store anything until a restart.
func TestProjectDirectoryRemovedWhileRunningIsRecreated(t *testing.T) {
	ts, _, cfg := tunedServer(t, nil)
	cs := connect(t, ts.URL+"/project/"+idA)
	create := func(name string) {
		callTool(t, cs, "create_entities", map[string]any{
			"entities": []map[string]any{{"name": name, "entityType": "person", "observations": []string{}}},
		})
	}

	create("Ada")
	dir := filepath.Join(cfg.ProjectsDir, idA)
	require.NoError(t, os.RemoveAll(dir))

	create("Grace")
	graph := callTool(t, cs, "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1, "the project starts over as an empty one")
	assert.FileExists(t, filepath.Join(dir, "project.json"), "the metadata is published again with the data")
}

// Many requests opening one project at once end up sharing one runtime, and none
// of them holds the registry's lock while it reads the disk.
func TestConcurrentOpensShareOneRuntime(t *testing.T) {
	root := t.TempDir()
	// A directory without metadata makes every open take the path that writes.
	require.NoError(t, os.MkdirAll(filepath.Join(root, idA), 0o755))
	registry := server.NewRegistry(root, 10, nil)

	const callers = 32
	got := make([]*server.Project, callers)
	var wg sync.WaitGroup
	for i := range got {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := registry.Get(idA)
			if assert.NoError(t, err) {
				got[i] = p
			}
		}()
	}
	wg.Wait()

	for i := range got {
		require.NotNil(t, got[i])
		assert.Same(t, got[0], got[i], "every caller must be given the same runtime")
	}
	assert.FileExists(t, filepath.Join(root, idA, "project.json"))
}

// A client that dies without ending its session must not hold it forever: an
// idle session is closed, and the client that comes back is told to start again.
func TestIdleSessionIsClosed(t *testing.T) {
	ts, _, _ := tunedServer(t, func(cfg *config.Config) { cfg.SessionTimeout = 150 * time.Millisecond })
	endpoint := ts.URL + "/project/" + idA

	cs := connect(t, endpoint)
	callTool(t, cs, "read_graph", map[string]any{})

	require.Eventually(t, func() bool {
		_, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "read_graph", Arguments: map[string]any{}})
		return err != nil
	}, 5*time.Second, 400*time.Millisecond, "an idle session must stop being served")

	// The project is unaffected: a new session is served at once.
	callTool(t, connect(t, endpoint), "read_graph", map[string]any{})
}

// A client that sends its headers and then stalls on the body is cut off, where
// before it could hold the connection and its handler open indefinitely.
func TestStalledRequestBodyIsCutOff(t *testing.T) {
	ts, _, _ := tunedServer(t, func(cfg *config.Config) { cfg.BodyReadTimeout = 200 * time.Millisecond })

	conn, err := net.Dial("tcp", ts.Listener.Addr().String())
	require.NoError(t, err)
	defer conn.Close()

	// Headers that promise more body than is ever sent.
	_, err = fmt.Fprintf(conn, "POST /project/%s HTTP/1.1\r\nHost: %s\r\nContent-Type: application/json\r\n"+
		"Accept: application/json, text/event-stream\r\nContent-Length: %d\r\n\r\n%s",
		idA, ts.Listener.Addr().String(), len(initialize), initialize[:20])
	require.NoError(t, err)

	start := time.Now()
	require.NoError(t, conn.SetReadDeadline(start.Add(5*time.Second)))
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	elapsed := time.Since(start)

	// Either outcome frees the server: an error status, or a closed connection.
	if err == nil {
		defer resp.Body.Close()
		assert.GreaterOrEqual(t, resp.StatusCode, 400, "a request whose body never arrived must not succeed")
	}
	assert.Less(t, elapsed, 3*time.Second, "the server must give up on a stalled body")
}

// The body deadline ends with the body. A session that outlives it many times
// over — with its standalone stream open and its connection reused — still works.
func TestBodyDeadlineDoesNotOutliveTheBody(t *testing.T) {
	ts, _, _ := tunedServer(t, func(cfg *config.Config) { cfg.BodyReadTimeout = 150 * time.Millisecond })

	// The default transport keeps the standalone SSE stream open, as a harness does.
	client := mcp.NewClient(&mcp.Implementation{Name: "deadline-test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: ts.URL + "/project/" + idA}, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })

	for i := 0; i < 3; i++ {
		time.Sleep(300 * time.Millisecond)
		callTool(t, cs, "create_entities", map[string]any{
			"entities": []map[string]any{{"name": fmt.Sprintf("e%d", i), "entityType": "t", "observations": []string{}}},
		})
	}
	graph := callTool(t, cs, "read_graph", map[string]any{})
	assert.Len(t, graph["entities"], 3)

	// A plain request on a reused keep-alive connection, after the deadline of the
	// request before it has long passed.
	for i := 0; i < 2; i++ {
		resp := post(t, ts.URL+"/project/"+idA, initialize)
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		time.Sleep(300 * time.Millisecond)
	}
}
