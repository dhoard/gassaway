// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tenant-isolation tests. These are the ones to read before believing the
// isolation claim: they try to reach one project's data through every surface
// the server has — tools, the resource, and another project's session — and
// they assert both the refusal and the absence of side effects.
package server_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// foreignSessionRequest sends body to path with headers, as a raw HTTP client
// would, so the transport's own session handling is exercised rather than the
// SDK client's.
func foreignSessionRequest(t *testing.T, method, endpoint, body, sessionID string, headers map[string]string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, endpoint, strings.NewReader(body))
	require.NoError(t, err)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if sessionID != "" {
		req.Header.Set("Mcp-Session-Id", sessionID)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(data)
}

// TestForeignSessionIsRefusedForEveryMethod proves that a session minted for
// project A cannot be driven at project B's URL, and that the attempt leaves
// project B uncreated.
func TestForeignSessionIsRefusedForEveryMethod(t *testing.T) {
	_, ts, cfg := newServer(t, 10)

	a := connect(t, ts.URL+"/project/"+idA)
	require.True(t, strings.HasPrefix(a.ID(), idA+"."), "session %q must be scoped to its project", a.ID())
	callTool(t, a, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "SecretOfA", "entityType": "t", "observations": []string{"private"}}},
	})

	body := `{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"read_graph","arguments":{}}}`
	for _, tc := range []struct {
		method  string
		body    string
		headers map[string]string
	}{
		{http.MethodPost, body, nil},
		{http.MethodGet, "", map[string]string{"Accept": "text/event-stream"}},
		{http.MethodDelete, "", nil},
	} {
		t.Run(tc.method, func(t *testing.T) {
			status, respBody := foreignSessionRequest(t, tc.method, ts.URL+"/project/"+idB, tc.body, a.ID(), tc.headers)
			assert.Equal(t, http.StatusNotFound, status, "%s must not be served under another project's URL", tc.method)
			assert.NotContains(t, respBody, "SecretOfA", "no graph content may cross the boundary")
		})
	}

	_, err := os.Stat(filepath.Join(cfg.ProjectsDir, idB))
	assert.True(t, os.IsNotExist(err), "a foreign session must not create the other project's storage")

	// A's session still works at A, and A's graph is untouched.
	graph := callTool(t, a, "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1)
	assert.Equal(t, "SecretOfA", graph["entities"].([]any)[0].(map[string]any)["name"])
}

// TestProjectCannotReachAnotherProjectsData drives every read and write path
// from B's own session and asserts that none of them sees A's graph.
func TestProjectCannotReachAnotherProjectsData(t *testing.T) {
	_, ts, _ := newServer(t, 10)

	a := connect(t, ts.URL+"/project/"+idA)
	b := connect(t, ts.URL+"/project/"+idB)

	callTool(t, a, "create_entities", map[string]any{
		"entities": []map[string]any{
			{"name": "SecretOfA", "entityType": "person", "observations": []string{"private observation"}},
		},
	})
	callTool(t, a, "create_relations", map[string]any{
		"relations": []map[string]any{{"from": "SecretOfA", "to": "SecretOfA", "relationType": "self"}},
	})

	// Read: every read tool.
	for _, name := range []string{"read_graph", "search_nodes", "open_nodes"} {
		args := map[string]any{}
		switch name {
		case "search_nodes":
			args["query"] = "SecretOfA"
		case "open_nodes":
			args["names"] = []string{"SecretOfA"}
		}
		got := callTool(t, b, name, args)
		assert.Empty(t, got["entities"], "%s must not see the other project's entities", name)
		assert.Empty(t, got["relations"], "%s must not see the other project's relations", name)
	}

	// Read: the resource.
	res, err := b.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "memory://knowledge-graph"})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.NotContains(t, res.Contents[0].Text, "SecretOfA")

	// Write: a delete of a name it cannot see removes nothing, anywhere.
	deleted := callTool(t, b, "delete_entities", map[string]any{"entityNames": []string{"SecretOfA"}})
	assert.Equal(t, true, deleted["success"])
	assert.Contains(t, deleted["message"], "Not found: SecretOfA")

	// Write: the same name created in B is a different entity, not a hijack of A's.
	callTool(t, b, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "SecretOfA", "entityType": "person", "observations": []string{"B's own"}}},
	})
	bGraph := callTool(t, b, "read_graph", map[string]any{})
	require.Len(t, bGraph["entities"], 1)
	assert.Equal(t, []any{"B's own"}, bGraph["entities"].([]any)[0].(map[string]any)["observations"])

	aGraph := callTool(t, a, "read_graph", map[string]any{})
	require.Len(t, aGraph["entities"], 1)
	assert.Equal(t, []any{"private observation"}, aGraph["entities"].([]any)[0].(map[string]any)["observations"],
		"a write in one project must not reach another project's entity of the same name")
	assert.Len(t, aGraph["relations"], 1)
}

// TestSessionsAreScopedToTheirProject checks the shape the binding relies on:
// two projects, two ranges of session IDs, no overlap.
func TestSessionsAreScopedToTheirProject(t *testing.T) {
	_, ts, _ := newServer(t, 10)
	a := connect(t, ts.URL+"/project/"+idA)
	b := connect(t, ts.URL+"/project/"+idB)

	assert.True(t, strings.HasPrefix(a.ID(), idA+"."), "a's session %q", a.ID())
	assert.True(t, strings.HasPrefix(b.ID(), idB+"."), "b's session %q", b.ID())
	assert.NotEqual(t, a.ID(), b.ID())

	// A session ID that names a project but is not one the server minted is a
	// session the server does not have.
	forged := idA + ".NOTAREALSESSION"
	status, _ := foreignSessionRequest(t, http.MethodPost, ts.URL+"/project/"+idA, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, forged, nil)
	assert.Equal(t, http.StatusNotFound, status, "the random suffix is the credential")

	// The project prefix alone does not authenticate anything: a session ID for a
	// hash that was never opened is still refused when used elsewhere.
	status, _ = foreignSessionRequest(t, http.MethodPost, ts.URL+"/project/"+idB, `{"jsonrpc":"2.0","id":1,"method":"ping"}`, forged, nil)
	assert.Equal(t, http.StatusNotFound, status)
}

// TestUnknownProjectIsNotCreatedByReading proves the other half of the isolation
// claim: a request for a project that holds nothing leaves nothing behind, so an
// unauthenticated caller cannot allocate storage by guessing hashes.
func TestUnknownProjectIsNotCreatedByReading(t *testing.T) {
	_, ts, cfg := newServer(t, 10)
	connect(t, ts.URL+"/project/"+idA)

	entries, err := os.ReadDir(cfg.ProjectsDir)
	require.NoError(t, err)
	for _, entry := range entries {
		assert.NotEqual(t, idA, entry.Name(), "reading a project must not create its directory")
	}
}

// TestHealthCountsStoredProjects proves the operator view does not enumerate
// tenant identities.
func TestHealthCountsStoredProjects(t *testing.T) {
	_, ts, cfg := newServer(t, 10)
	connect(t, ts.URL+"/project/"+idA)

	payload := getJSON(t, ts.URL+"/healthz")
	assert.Equal(t, float64(0), payload["projects"], "a project with no stored data is not counted")
	encoded, err := json.Marshal(payload)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), idA, "the health payload must not name projects")

	_, err = os.Stat(filepath.Join(cfg.ProjectsDir, idA))
	assert.True(t, os.IsNotExist(err))
}
