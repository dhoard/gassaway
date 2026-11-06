// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file registers the memory://knowledge-graph resource.
//
// The resource is the read-only face of the same graph read_graph returns, kept
// because it is part of the reference server's published surface: a client that
// reads resources (rather than calling tools) sees the graph without a tool
// call. The server does not implement resource subscriptions in 0.1.0; the
// design records that as a deliberate omission, since the only consumer uses
// tools.
package memory

import (
	"context"
	"encoding/json"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/graph"
)

// ResourceURI is the MCP resource identifier for a project's whole graph.
const ResourceURI = "memory://knowledge-graph"

// RegisterResource adds the knowledge-graph resource to s.
func RegisterResource(s *mcp.Server, store *graph.Store) {
	s.AddResource(&mcp.Resource{
		URI:         ResourceURI,
		Name:        "knowledge-graph",
		Title:       "Knowledge Graph",
		Description: "The full knowledge graph with all entities and relations",
		MIMEType:    "application/json",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		g, err := store.Read()
		if err != nil {
			return nil, err
		}
		text, err := json.MarshalIndent(g, "", "  ")
		if err != nil {
			return nil, err
		}
		return &mcp.ReadResourceResult{
			Contents: []*mcp.ResourceContents{{
				URI:      ResourceURI,
				MIMEType: "application/json",
				Text:     string(text),
			}},
		}, nil
	})
}
