// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Package memory exposes one project's graph as the memory server's MCP
// surface: the nine tools and the knowledge-graph resource.
//
// The tool names, argument shapes and result shapes follow the reference memory
// server, so prompts and clients written against it keep working. The typed
// handlers use the SDK's generic AddTool, which infers the JSON schemas,
// validates arguments before the handler runs, and turns a returned error into a
// tool error result the model can read and correct.
package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/graph"
)

// MaxSearchQuery is the longest accepted search_nodes query, matching the
// reference server's limit.
const MaxSearchQuery = 2048

// Entity is the tool-facing view of one node.
type Entity struct {
	Name         string   `json:"name" jsonschema:"the name of the entity"`
	EntityType   string   `json:"entityType" jsonschema:"the type of the entity"`
	Observations []string `json:"observations" jsonschema:"an array of observation contents associated with the entity"`
}

// ReadEntity is the tool-facing view of one node as a read tool returns it.
// ObservationCount is present only when the call gave an observationLimit, so a
// count above len(observations) tells the caller that observations were left out.
type ReadEntity struct {
	Name             string   `json:"name" jsonschema:"the name of the entity"`
	EntityType       string   `json:"entityType" jsonschema:"the type of the entity"`
	Observations     []string `json:"observations" jsonschema:"the observations returned for this entity"`
	ObservationCount *int     `json:"observationCount,omitempty" jsonschema:"how many observations the entity holds in total; present only when observationLimit was given"`
}

// Relation is the tool-facing view of one directed edge.
type Relation struct {
	From         string `json:"from" jsonschema:"the name of the entity where the relation starts"`
	To           string `json:"to" jsonschema:"the name of the entity where the relation ends"`
	RelationType string `json:"relationType" jsonschema:"the type of the relation"`
}

type createEntitiesIn struct {
	Entities []Entity `json:"entities" jsonschema:"the entities to create"`
}

type createEntitiesOut struct {
	Entities []Entity `json:"entities"`
	Skipped  []string `json:"skipped,omitempty"`
}

type createRelationsIn struct {
	Relations []Relation `json:"relations" jsonschema:"the relations to create"`
}

type createRelationsOut struct {
	Relations []Relation `json:"relations"`
}

type addObservationsIn struct {
	Observations []observationAdd `json:"observations" jsonschema:"the observations to add"`
}

type observationAdd struct {
	EntityName string   `json:"entityName" jsonschema:"the entity to extend"`
	Contents   []string `json:"contents" jsonschema:"the observation contents to add"`
}

type addObservationsOut struct {
	Results []observationAddResult `json:"results"`
}

type observationAddResult struct {
	EntityName        string   `json:"entityName"`
	AddedObservations []string `json:"addedObservations"`
}

type deleteEntitiesIn struct {
	EntityNames []string `json:"entityNames" jsonschema:"the entity names to delete"`
}

type deleteEntitiesOut struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type deleteObservationsIn struct {
	Deletions []observationDelete `json:"deletions" jsonschema:"the observations to delete"`
}

type observationDelete struct {
	EntityName   string   `json:"entityName" jsonschema:"the entity whose observations are removed"`
	Observations []string `json:"observations" jsonschema:"the observation contents to remove"`
}

type deleteObservationsOut struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

type deleteRelationsIn struct {
	Relations []Relation `json:"relations" jsonschema:"the relations to delete"`
}

type deleteRelationsOut struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// readGraphIn, searchNodesIn and openNodesIn are the read tools' arguments. A
// pointer means "not given", which is what makes observationCount absent rather
// than zero when no observation limit was asked for.
type readGraphIn struct {
	EntityTypes      []string `json:"entityTypes,omitempty"`
	ObservationLimit *int     `json:"observationLimit,omitempty"`
}

type searchNodesIn struct {
	Query            string `json:"query"`
	Limit            int    `json:"limit,omitempty"`
	ObservationLimit *int   `json:"observationLimit,omitempty"`
}

type openNodesIn struct {
	Names            []string `json:"names"`
	ObservationLimit *int     `json:"observationLimit,omitempty"`
}

type graphOut struct {
	Entities  []ReadEntity `json:"entities"`
	Relations []Relation   `json:"relations"`
}

// entityView converts one stored entity to its tool-facing shape. Observations
// are always an array, never null: that is what the published schema promises.
func entityView(e graph.Entity) Entity {
	observations := e.Observations
	if observations == nil {
		observations = []string{}
	}
	return Entity{Name: e.Name, EntityType: e.EntityType, Observations: observations}
}

// relationView converts one stored relation to its tool-facing shape.
func relationView(r graph.Relation) Relation {
	return Relation{From: r.From, To: r.To, RelationType: r.RelationType}
}

// readEntityView converts one stored read result to its tool-facing shape.
func readEntityView(e graph.ReadEntity) ReadEntity {
	observations := e.Observations
	if observations == nil {
		observations = []string{}
	}
	return ReadEntity{
		Name:             e.Name,
		EntityType:       e.EntityType,
		Observations:     observations,
		ObservationCount: e.ObservationCount,
	}
}

// toGraphOut converts a read result into the tool-facing shape.
func toGraphOut(g graph.ReadGraph) graphOut {
	out := graphOut{Entities: make([]ReadEntity, 0, len(g.Entities)), Relations: make([]Relation, 0, len(g.Relations))}
	for _, e := range g.Entities {
		out.Entities = append(out.Entities, readEntityView(e))
	}
	for _, r := range g.Relations {
		out.Relations = append(out.Relations, relationView(r))
	}
	return out
}

// readOptionsFrom builds the store's options from a tool's arguments.
func readOptionsFrom(entityTypes []string, limit int, observationLimit *int) graph.ReadOptions {
	return graph.ReadOptions{EntityTypes: entityTypes, Limit: limit, ObservationLimit: observationLimit}
}

// messageResult returns a result whose text is the human-readable summary, with
// the same values in structured content. The delete tools report like the
// reference server does: a success flag and one sentence saying what happened, so
// a model that reads either form sees the same thing.
func messageResult(message string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: message}}}
}

func toGraphEntities(entities []Entity) []graph.Entity {
	out := make([]graph.Entity, 0, len(entities))
	for _, e := range entities {
		obs := e.Observations
		if obs == nil {
			obs = []string{}
		}
		out = append(out, graph.Entity{Name: e.Name, EntityType: e.EntityType, Observations: obs})
	}
	return out
}

func toGraphRelations(relations []Relation) []graph.Relation {
	out := make([]graph.Relation, 0, len(relations))
	for _, r := range relations {
		out = append(out, graph.Relation{From: r.From, To: r.To, RelationType: r.RelationType})
	}
	return out
}

// Register adds the memory tools to s, all reading and writing the given store.
// The order here is the order the tool list is published in.
func Register(s *mcp.Server, store *graph.Store) {
	registerCreateEntities(s, store)
	registerCreateRelations(s, store)
	registerAddObservations(s, store)
	registerDeleteEntities(s, store)
	registerDeleteObservations(s, store)
	registerDeleteRelations(s, store)
	registerReadGraph(s, store)
	registerSearchNodes(s, store)
	registerOpenNodes(s, store)
}

// registerCreateEntities registers the create_entities tool.
func registerCreateEntities(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "create_entities",
		Title:        "Create Entities",
		InputSchema:  createEntitiesSchema,
		OutputSchema: createEntitiesResultSchema,
		Description: "Add one or more entities to this project's graph. A name that is already present, or that " +
			"appears twice in the same call, is not created again: its observations are dropped, the name is " +
			"reported under `skipped`, and a second text item names it. Use add_observations to extend an entity " +
			"that already exists.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in createEntitiesIn) (*mcp.CallToolResult, createEntitiesOut, error) {
		result, err := store.CreateEntities(toGraphEntities(in.Entities))
		if err != nil {
			return nil, createEntitiesOut{}, err
		}
		out := createEntitiesOut{Entities: make([]Entity, 0, len(result.Created)), Skipped: result.Skipped}
		for _, e := range result.Created {
			out.Entities = append(out.Entities, entityView(e))
		}
		if len(result.Skipped) == 0 {
			return nil, out, nil
		}
		// A skipped entity keeps none of its observations, so an agent that reads
		// only the structured result can believe they were stored. Say so in the
		// text the model reads, as the reference server does.
		encoded, err := json.Marshal(out)
		if err != nil {
			return nil, createEntitiesOut{}, err
		}
		return &mcp.CallToolResult{Content: []mcp.Content{
			&mcp.TextContent{Text: string(encoded)},
			&mcp.TextContent{Text: skippedNotice(result.Skipped)},
		}}, out, nil
	})
}

// registerCreateRelations registers the create_relations tool.
func registerCreateRelations(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "create_relations",
		Title:        "Create Relations",
		InputSchema:  createRelationsSchema,
		OutputSchema: createRelationsResultSchema,
		Description: "Connect existing entities with directed edges. Both endpoints must exist already or the call " +
			"fails and nothing is written; an edge that is already present is left alone, not duplicated. " +
			"Arguments are `relations`: an array of {from, to, relationType}.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in createRelationsIn) (*mcp.CallToolResult, createRelationsOut, error) {
		created, err := store.CreateRelations(toGraphRelations(in.Relations))
		if err != nil {
			return nil, createRelationsOut{}, err
		}
		out := createRelationsOut{Relations: make([]Relation, 0, len(created))}
		for _, r := range created {
			out.Relations = append(out.Relations, relationView(r))
		}
		return nil, out, nil
	})
}

// registerAddObservations registers the add_observations tool.
func registerAddObservations(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "add_observations",
		Title:        "Add Observations",
		InputSchema:  addObservationsSchema,
		OutputSchema: addObservationsResultSchema,
		Description: "Attach more observation strings to entities that already exist. Strings an entity already " +
			"carries are skipped rather than repeated, and a name that is missing fails the call. Arguments are " +
			"`observations`: an array of {entityName, contents}.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in addObservationsIn) (*mcp.CallToolResult, addObservationsOut, error) {
		adds := make([]graph.ObservationAdd, 0, len(in.Observations))
		for _, o := range in.Observations {
			adds = append(adds, graph.ObservationAdd{EntityName: o.EntityName, Contents: o.Contents})
		}
		results, err := store.AddObservations(adds)
		if err != nil {
			return nil, addObservationsOut{}, err
		}
		out := addObservationsOut{Results: make([]observationAddResult, 0, len(results))}
		for _, r := range results {
			out.Results = append(out.Results, observationAddResult{EntityName: r.EntityName, AddedObservations: r.AddedObservations})
		}
		return nil, out, nil
	})
}

// registerDeleteEntities registers the delete_entities tool.
func registerDeleteEntities(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "delete_entities",
		Title:        "Delete Entities",
		InputSchema:  deleteEntitiesSchema,
		OutputSchema: deleteEntitiesResultSchema,
		Description: "Remove entities, together with every edge that touches them. Names that are not present do " +
			"not fail the call: the result is {success, message}, and the message says how many were deleted and " +
			"which were not found.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deleteEntitiesIn) (*mcp.CallToolResult, deleteEntitiesOut, error) {
		result, err := store.DeleteEntities(in.EntityNames)
		if err != nil {
			return nil, deleteEntitiesOut{}, err
		}
		message := "Entities deleted successfully"
		if len(result.NotFound) > 0 {
			message = fmt.Sprintf("Deleted %d of %d entities. Not found: %s",
				len(result.Deleted), len(in.EntityNames), strings.Join(result.NotFound, ", "))
		}
		out := deleteEntitiesOut{Success: true, Message: message}
		return messageResult(message), out, nil
	})
}

// registerDeleteObservations registers the delete_observations tool.
func registerDeleteObservations(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "delete_observations",
		Title:        "Delete Observations",
		InputSchema:  deleteObservationsSchema,
		OutputSchema: deleteObservationsResultSchema,
		Description: "Remove chosen observation strings from entities. The result is {success, message}, and the " +
			"message says how many were removed and which entity names were missing.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deleteObservationsIn) (*mcp.CallToolResult, deleteObservationsOut, error) {
		deletes := make([]graph.ObservationDelete, 0, len(in.Deletions))
		for _, d := range in.Deletions {
			deletes = append(deletes, graph.ObservationDelete{EntityName: d.EntityName, Observations: d.Observations})
		}
		result, err := store.DeleteObservations(deletes)
		if err != nil {
			return nil, deleteObservationsOut{}, err
		}
		requested := 0
		for _, d := range deletes {
			requested += len(d.Observations)
		}
		message := "Observations deleted successfully"
		if result.DeletedCount != requested {
			message = fmt.Sprintf("Deleted %d of %d observations.", result.DeletedCount, requested)
			if len(result.MissingEntities) > 0 {
				message += fmt.Sprintf(" Entities not found: %s", strings.Join(result.MissingEntities, ", "))
			}
		}
		out := deleteObservationsOut{Success: true, Message: message}
		return messageResult(message), out, nil
	})
}

// registerDeleteRelations registers the delete_relations tool.
func registerDeleteRelations(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "delete_relations",
		Title:        "Delete Relations",
		InputSchema:  deleteRelationsSchema,
		OutputSchema: deleteRelationsResultSchema,
		Description:  "Remove edges, matching on from, to and relationType together. The result says how many matched.",
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: false, DestructiveHint: boolPtr(true), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in deleteRelationsIn) (*mcp.CallToolResult, deleteRelationsOut, error) {
		deleted, err := store.DeleteRelations(toGraphRelations(in.Relations))
		if err != nil {
			return nil, deleteRelationsOut{}, err
		}
		message := "Relations deleted successfully"
		if deleted != len(in.Relations) {
			message = fmt.Sprintf("Deleted %d of %d relations. The rest matched nothing.", deleted, len(in.Relations))
		}
		out := deleteRelationsOut{Success: true, Message: message}
		return messageResult(message), out, nil
	})
}

// registerReadGraph registers the read_graph tool.
func registerReadGraph(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "read_graph",
		Title:        "Read Graph",
		InputSchema:  readGraphSchema,
		OutputSchema: readGraphResultSchema,
		Description:  "Return every entity and edge in this project's graph.",
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in readGraphIn) (*mcp.CallToolResult, graphOut, error) {
		g, err := store.ReadGraph(readOptionsFrom(in.EntityTypes, 0, in.ObservationLimit))
		if err != nil {
			return nil, graphOut{}, err
		}
		return nil, toGraphOut(g), nil
	})
}

// registerSearchNodes registers the search_nodes tool.
func registerSearchNodes(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "search_nodes",
		Title:        "Search Nodes",
		InputSchema:  searchNodesSchema,
		OutputSchema: searchNodesResultSchema,
		Description: "Case-insensitive search across entity names, entity types and observation text. Edges with " +
			"either endpoint among the matches are included. Pass limit to keep only the first matches in stored " +
			"order, and observationLimit to cap each entity's observations (adding observationCount).",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in searchNodesIn) (*mcp.CallToolResult, graphOut, error) {
		if len(in.Query) > MaxSearchQuery {
			return nil, graphOut{}, fmt.Errorf("query is longer than %d characters", MaxSearchQuery)
		}
		g, err := store.SearchNodes(in.Query, readOptionsFrom(nil, in.Limit, in.ObservationLimit))
		if err != nil {
			return nil, graphOut{}, err
		}
		return nil, toGraphOut(g), nil
	})
}

// registerOpenNodes registers the open_nodes tool.
func registerOpenNodes(s *mcp.Server, store *graph.Store) {
	mcp.AddTool(s, &mcp.Tool{
		Name:         "open_nodes",
		Title:        "Open Nodes",
		InputSchema:  openNodesSchema,
		OutputSchema: openNodesResultSchema,
		Description:  "Return the named entities and every edge touching them. Names that are not present are ignored.",
		Annotations:  &mcp.ToolAnnotations{ReadOnlyHint: true, DestructiveHint: boolPtr(false), IdempotentHint: true, OpenWorldHint: boolPtr(false)},
	}, func(_ context.Context, _ *mcp.CallToolRequest, in openNodesIn) (*mcp.CallToolResult, graphOut, error) {
		g, err := store.OpenNodes(in.Names, readOptionsFrom(nil, 0, in.ObservationLimit))
		if err != nil {
			return nil, graphOut{}, err
		}
		return nil, toGraphOut(g), nil
	})
}

func boolPtr(b bool) *bool { return &b }

// skippedNotice tells the model which names create_entities did not create and
// what to do about it.
func skippedNotice(skipped []string) string {
	noun, verb, possessive := "entity that already exists", "was", "Its"
	if len(skipped) != 1 {
		noun, verb, possessive = "entities that already exist", "were", "Their"
	}
	return fmt.Sprintf("Skipped %d %s: %s. %s observations %s not added; use add_observations for existing entities.",
		len(skipped), noun, strings.Join(skipped, ", "), possessive, verb)
}
