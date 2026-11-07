// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests that pin the published wire contract to the committed capture of the
// reference memory server, and that pin the accepted payload shapes that
// motivated authoring the schemas in the first place.
//
// The comparison is structural: property names, types, required lists and
// openness are compared; descriptions and prose are not, because they are
// model-facing text rather than contract. testdata/PROVENANCE.md records each
// deliberate difference this file asserts.
package memory_test

import (
	"context"
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// referenceCapture is the subset of the committed capture this file reads.
type referenceCapture struct {
	Tools struct {
		Result struct {
			Tools []struct {
				Name         string          `json:"name"`
				Title        string          `json:"title"`
				InputSchema  json.RawMessage `json:"inputSchema"`
				OutputSchema json.RawMessage `json:"outputSchema"`
				Annotations  struct {
					ReadOnlyHint    bool  `json:"readOnlyHint"`
					DestructiveHint *bool `json:"destructiveHint"`
					IdempotentHint  bool  `json:"idempotentHint"`
					OpenWorldHint   *bool `json:"openWorldHint"`
				} `json:"annotations"`
			} `json:"tools"`
		} `json:"result"`
	} `json:"tools"`
	Resources struct {
		Result struct {
			Resources []struct {
				URI         string `json:"uri"`
				Name        string `json:"name"`
				Title       string `json:"title"`
				Description string `json:"description"`
				MIMEType    string `json:"mimeType"`
			} `json:"resources"`
		} `json:"result"`
	} `json:"resources"`
}

func loadCapture(t *testing.T) referenceCapture {
	t.Helper()
	data, err := os.ReadFile("testdata/reference-tools.json")
	require.NoError(t, err)
	var cap referenceCapture
	require.NoError(t, json.Unmarshal(data, &cap))
	require.Len(t, cap.Tools.Result.Tools, 9)
	return cap
}

// schemaShape is the contract-bearing part of a tool's argument schema: what
// properties exist, what type each is, which are required, and what an array's
// items require. Openness is carried by the absence of additionalProperties in
// the normalised output, so a closed object shows up as a difference.
func schemaShape(t *testing.T, raw any) map[string]any {
	t.Helper()
	data, err := json.Marshal(raw)
	require.NoError(t, err)
	var schema map[string]any
	require.NoError(t, json.Unmarshal(data, &schema))
	return objectShape(t, schema)
}

func objectShape(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{
		"type": schema["type"],
	}
	if closed, ok := schema["additionalProperties"]; ok {
		out["additionalProperties"] = closed
	}
	if required, ok := schema["required"].([]any); ok {
		out["required"] = sortedStrings(required)
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		shapes := make(map[string]any, len(props))
		for name, prop := range props {
			if m, ok := prop.(map[string]any); ok {
				shapes[name] = propertyShape(t, m)
			} else {
				shapes[name] = prop
			}
		}
		out["properties"] = shapes
	}
	return out
}

func propertyShape(t *testing.T, prop map[string]any) map[string]any {
	t.Helper()
	out := map[string]any{"type": prop["type"]}
	switch prop["type"] {
	case "array":
		if items, ok := prop["items"].(map[string]any); ok {
			if items["type"] == "object" {
				out["items"] = objectShape(t, items)
			} else {
				out["items"] = map[string]any{"type": items["type"]}
			}
		}
	case "object":
		out = objectShape(t, prop)
	}
	return out
}

func sortedStrings(list []any) []string {
	out := make([]string, 0, len(list))
	for _, v := range list {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// TestInputSchemasAreTheReferenceContract is the pin: every published argument
// schema must be the reference's schema restricted to the arguments this server
// implements, and must stay open.
func TestInputSchemasAreTheReferenceContract(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Tools, 9)

	cap := loadCapture(t)
	reference := make(map[string]map[string]any, len(cap.Tools.Result.Tools))
	for _, tool := range cap.Tools.Result.Tools {
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.InputSchema, &schema))
		reference[tool.Name] = schema
	}

	outputs := make(map[string]map[string]any, len(cap.Tools.Result.Tools))
	for _, tool := range cap.Tools.Result.Tools {
		var schema map[string]any
		require.NoError(t, json.Unmarshal(tool.OutputSchema, &schema))
		outputs[tool.Name] = schema
	}

	for _, tool := range res.Tools {
		t.Run(tool.Name, func(t *testing.T) {
			want, ok := reference[tool.Name]
			require.True(t, ok, "tool %s is not in the reference capture", tool.Name)

			mine := schemaShape(t, tool.InputSchema)
			assert.NotEqual(t, false, mine["additionalProperties"],
				"the argument object must stay open: a closed object reports additional properties before reporting a missing required one")

			assert.Equal(t, schemaShape(t, want), mine,
				"published schema for %s diverges from the reference contract", tool.Name)

			wantOut, ok := outputs[tool.Name]
			require.True(t, ok, "tool %s has no output schema in the reference capture", tool.Name)
			require.NotNil(t, tool.OutputSchema, "tool %s publishes no output schema", tool.Name)
			assert.Equal(t, schemaShape(t, wantOut), schemaShape(t, tool.OutputSchema),
				"published output schema for %s diverges from the reference contract", tool.Name)
		})
	}
}

// TestToolMetadataMatchesTheReference pins titles and every annotation hint.
// These are advisory to a client, which is exactly why a wrong value costs the
// model a safe retry or invites a destructive confirmation it did not need.
func TestToolMetadataMatchesTheReference(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListTools(context.Background(), nil)
	require.NoError(t, err)

	cap := loadCapture(t)
	for _, want := range cap.Tools.Result.Tools {
		var got *mcp.Tool
		for _, tool := range res.Tools {
			if tool.Name == want.Name {
				got = tool
				break
			}
		}
		require.NotNil(t, got, "tool %s is missing", want.Name)
		require.NotNil(t, got.Annotations, "tool %s publishes no annotations", want.Name)

		assert.Equal(t, want.Title, got.Title, "tool %s title", want.Name)
		assert.Equal(t, want.Annotations.ReadOnlyHint, got.Annotations.ReadOnlyHint, "tool %s readOnlyHint", want.Name)
		assert.Equal(t, want.Annotations.IdempotentHint, got.Annotations.IdempotentHint, "tool %s idempotentHint", want.Name)
		require.NotNil(t, got.Annotations.DestructiveHint, "tool %s destructiveHint", want.Name)
		assert.Equal(t, *want.Annotations.DestructiveHint, *got.Annotations.DestructiveHint, "tool %s destructiveHint", want.Name)
		require.NotNil(t, got.Annotations.OpenWorldHint, "tool %s openWorldHint must be explicit: the protocol default is true, and a memory graph is a closed world", want.Name)
		assert.Equal(t, *want.Annotations.OpenWorldHint, *got.Annotations.OpenWorldHint, "tool %s openWorldHint", want.Name)
	}
}

// TestResourceMatchesTheReference pins the resource's serving shape.
func TestResourceMatchesTheReference(t *testing.T) {
	cs, _ := connect(t)
	res, err := cs.ListResources(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, res.Resources, 1)

	cap := loadCapture(t)
	require.Len(t, cap.Resources.Result.Resources, 1)
	want := cap.Resources.Result.Resources[0]
	got := res.Resources[0]

	assert.Equal(t, want.URI, got.URI)
	assert.Equal(t, want.Name, got.Name)
	assert.Equal(t, want.Title, got.Title)
	assert.Equal(t, want.Description, got.Description)
	assert.Equal(t, want.MIMEType, got.MIMEType)
}

// TestAcceptedPayloads pins the payload shapes the authored schemas were
// written for: an unknown envelope field is now ignored rather than refused,
// and a payload that is missing a required property says so.
func TestAcceptedPayloads(t *testing.T) {
	cs, _ := connect(t)

	created := callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{"writes Go"}}},
	})
	require.Len(t, created["entities"], 1)

	// An extra envelope field is accepted and ignored.
	extra := callTool(t, cs, "add_observations", map[string]any{
		"observations": []map[string]any{{"entityName": "Ada", "contents": []string{"reads specs"}}},
		"unexpected":   "ignored",
	})
	require.Len(t, extra["results"], 1)

	// The flattened single-observation payload is still rejected, and the message
	// names the missing property instead of complaining about additional ones.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "add_observations",
		Arguments: map[string]any{"entityName": "Ada", "contents": []string{"lost"}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	text := toolText(res)
	assert.Contains(t, text, "observations", "the diagnostic must name the missing property")
	assert.NotContains(t, text, "additional", "a closed object hides the actionable message behind additionalProperties")

	// A missing property inside an item is reported the same way.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_entities",
		Arguments: map[string]any{"entities": []map[string]any{{"name": "Grace"}}},
	})
	require.NoError(t, err)
	require.True(t, res.IsError)
	assert.Contains(t, toolText(res), "entityType")

	// Nothing above wrote anything it should not have.
	graph := callTool(t, cs, "read_graph", map[string]any{})
	require.Len(t, graph["entities"], 1)
	assert.Equal(t, []any{"writes Go", "reads specs"}, graph["entities"].([]any)[0].(map[string]any)["observations"])
}

// TestSkippedNoticeNamesTheEntities pins R-06: a skipped entity keeps none of
// its observations, so the model is told in the text it reads.
func TestSkippedNoticeNamesTheEntities(t *testing.T) {
	cs, _ := connect(t)
	callTool(t, cs, "create_entities", map[string]any{
		"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{}}},
	})

	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "create_entities",
		Arguments: map[string]any{
			"entities": []map[string]any{{"name": "Ada", "entityType": "person", "observations": []string{"lost"}}},
		},
	})
	require.NoError(t, err)
	require.False(t, res.IsError)
	require.Len(t, res.Content, 2, "a skipped entity must produce the result plus a notice")

	notice, ok := res.Content[1].(*mcp.TextContent)
	require.True(t, ok)
	assert.Contains(t, notice.Text, "Ada")
	assert.Contains(t, notice.Text, "add_observations")

	// A call with nothing skipped carries no notice.
	res, err = cs.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "create_entities",
		Arguments: map[string]any{"entities": []map[string]any{{"name": "Grace", "entityType": "person", "observations": []string{}}}},
	})
	require.NoError(t, err)
	require.Len(t, res.Content, 1)

	graph := callTool(t, cs, "read_graph", map[string]any{})
	for _, e := range graph["entities"].([]any) {
		assert.NotContains(t, strings.Join(toStrings(e.(map[string]any)["observations"]), ","), "lost",
			"a skipped entity's observations must not be stored")
	}
}

func toStrings(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// callTool invokes one tool and returns its structured result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	require.NoError(t, err)
	require.False(t, res.IsError, "tool %s failed: %s", name, toolText(res))
	encoded, err := json.Marshal(res.StructuredContent)
	require.NoError(t, err)
	var out map[string]any
	require.NoError(t, json.Unmarshal(encoded, &out))
	return out
}

// toolText is the textual part of a tool result.
func toolText(res *mcp.CallToolResult) string {
	var b strings.Builder
	for _, content := range res.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			b.WriteString(text.Text)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// TestReadToolOptions pins the narrowing arguments the read tools now accept. A
// read without them returns what it always did, including no observationCount:
// the field means "observations were left out", so it must not appear when none
// were.
func TestReadToolOptions(t *testing.T) {
	cs, _ := connect(t)
	callTool(t, cs, "create_entities", map[string]any{"entities": []map[string]any{
		{"name": "Ada", "entityType": "person", "observations": []string{"one", "two", "three"}},
		{"name": "Grace", "entityType": "person", "observations": []string{"four"}},
		{"name": "gassaway", "entityType": "project", "observations": []string{}},
	}})
	callTool(t, cs, "create_relations", map[string]any{"relations": []map[string]any{
		{"from": "Ada", "to": "gassaway", "relationType": "works_on"},
		{"from": "Ada", "to": "Grace", "relationType": "knows"},
	}})

	entities := func(out map[string]any) []map[string]any {
		list := out["entities"].([]any)
		result := make([]map[string]any, 0, len(list))
		for _, e := range list {
			result = append(result, e.(map[string]any))
		}
		return result
	}

	// No options: everything, and no observationCount.
	all := callTool(t, cs, "read_graph", map[string]any{})
	require.Len(t, all["entities"], 3)
	for _, e := range entities(all) {
		assert.NotContains(t, e, "observationCount", "a read that returned every observation reports no count")
	}

	// entityTypes keeps only that type and the relations touching it.
	people := callTool(t, cs, "read_graph", map[string]any{"entityTypes": []string{"person"}})
	require.Len(t, people["entities"], 2)
	assert.Len(t, people["relations"], 2, "relations touching a kept entity are returned")

	// observationLimit caps each entity and reports the total.
	limited := callTool(t, cs, "read_graph", map[string]any{"observationLimit": 2})
	require.Len(t, limited["entities"], 3)
	for _, e := range entities(limited) {
		assert.LessOrEqual(t, len(toStrings(e["observations"])), 2, "entity %s", e["name"])
		require.Contains(t, e, "observationCount")
	}
	assert.Equal(t, float64(3), entities(limited)[0]["observationCount"])

	// observationLimit 0 is the cheap overview: names, types and counts only.
	overview := callTool(t, cs, "read_graph", map[string]any{"observationLimit": 0})
	for _, e := range entities(overview) {
		assert.Empty(t, toStrings(e["observations"]), "entity %s", e["name"])
		assert.NotNil(t, e["observationCount"])
	}

	// limit bounds the number of matches; relations follow the kept entities.
	one := callTool(t, cs, "search_nodes", map[string]any{"query": "a", "limit": 1})
	assert.Len(t, one["entities"], 1)

	opened := callTool(t, cs, "open_nodes", map[string]any{"names": []string{"Ada"}, "observationLimit": 1})
	require.Len(t, opened["entities"], 1)
	ada := entities(opened)[0]
	assert.Equal(t, []any{"one"}, ada["observations"])
	assert.Equal(t, float64(3), ada["observationCount"])
	assert.Len(t, opened["relations"], 2, "open_nodes returns every relation touching a requested entity")
}

// TestDeleteToolsReportLikeTheReference pins the result shape of the three
// destructive tools: a success flag and one sentence.
func TestDeleteToolsReportLikeTheReference(t *testing.T) {
	cs, _ := connect(t)
	callTool(t, cs, "create_entities", map[string]any{"entities": []map[string]any{
		{"name": "Ada", "entityType": "person", "observations": []string{"one", "two"}},
		{"name": "Grace", "entityType": "person", "observations": []string{}},
	}})
	callTool(t, cs, "create_relations", map[string]any{"relations": []map[string]any{
		{"from": "Ada", "to": "Grace", "relationType": "knows"},
	}})

	obs := callTool(t, cs, "delete_observations", map[string]any{"deletions": []map[string]any{
		{"entityName": "Ada", "observations": []string{"one", "absent"}},
		{"entityName": "nobody", "observations": []string{"x"}},
	}})
	assert.Equal(t, true, obs["success"])
	assert.Contains(t, obs["message"], "Deleted 1 of 3 observations")
	assert.Contains(t, obs["message"], "nobody")

	rels := callTool(t, cs, "delete_relations", map[string]any{"relations": []map[string]any{
		{"from": "Ada", "to": "Grace", "relationType": "knows"},
		{"from": "Ada", "to": "Grace", "relationType": "missing"},
	}})
	assert.Equal(t, true, rels["success"])
	assert.Contains(t, rels["message"], "Deleted 1 of 2 relations")

	ents := callTool(t, cs, "delete_entities", map[string]any{"entityNames": []string{"Ada", "nobody"}})
	assert.Equal(t, true, ents["success"])
	assert.Contains(t, ents["message"], "Deleted 1 of 2 entities")
	assert.Contains(t, ents["message"], "nobody")

	all := callTool(t, cs, "delete_entities", map[string]any{"entityNames": []string{"Grace"}})
	assert.Equal(t, true, all["success"])
	assert.Equal(t, "Entities deleted successfully", all["message"])
}
