// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for the MCP surface itself, over an in-memory transport: the nine tool
// names, the search-length guard that only this layer enforces, and the resource.
// Behavior of the tools against a real graph is covered by the server and CLI
// tiers; this file pins the surface those tiers assume.
package memory_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/graph"
	"github.com/dhoard/gassaway/src/memory"
)

func connect(t *testing.T) (*mcp.ClientSession, *graph.Store) {
	t.Helper()
	ctx := context.Background()

	store := graph.NewStore(filepath.Join(t.TempDir(), "memory.jsonl"))
	server := mcp.NewServer(&mcp.Implementation{Name: "gassaway-test", Version: "0"}, nil)
	memory.Register(server, store)
	memory.RegisterResource(server, store)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _, _ = server.Connect(ctx, serverTransport, nil) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientTransport, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cs.Close() })
	return cs, store
}

func TestToolsListed(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	names := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		names = append(names, tool.Name)
		assert.NotEmpty(t, tool.Description, "tool %s needs a description for the model", tool.Name)
		assert.NotNil(t, tool.InputSchema, "tool %s needs an input schema", tool.Name)
	}
	assert.ElementsMatch(t, []string{
		"create_entities", "create_relations", "add_observations",
		"delete_entities", "delete_observations", "delete_relations",
		"read_graph", "search_nodes", "open_nodes",
	}, names)
}

func TestSearchQueryLengthIsGuarded(t *testing.T) {
	cs, _ := connect(t)
	tooLong := strings.Repeat("a", memory.MaxSearchQuery+1)

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_nodes",
		Arguments: map[string]any{"query": tooLong},
	})
	require.NoError(t, err)
	assert.True(t, res.IsError, "an over-long query must be a tool error")

	ok, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "search_nodes",
		Arguments: map[string]any{"query": "short"},
	})
	require.NoError(t, err)
	assert.False(t, ok.IsError)
}

func TestResourceDescribesTheGraph(t *testing.T) {
	cs, store := connect(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: memory.ResourceURI})
	require.NoError(t, err)
	require.Len(t, res.Contents, 1)
	assert.Equal(t, "application/json", res.Contents[0].MIMEType)
	assert.Contains(t, res.Contents[0].Text, "Ada")
}
