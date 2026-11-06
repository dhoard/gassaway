// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Package graph holds the knowledge-graph model and its JSONL store.
//
// The on-disk format is the reference memory server's, so an existing
// memory.jsonl can be served without conversion: one JSON object per line, with
// a "type" discriminator of "entity" or "relation". Two properties of the
// reference are deliberately preserved:
//
//   - Lines that cannot be understood (malformed JSON, a missing required
//     field, an unknown record type) are kept out of the graph but written back
//     unchanged by the next save, so an unrelated write never deletes them.
//   - Fields the model does not know (a createdAt written by another client)
//     survive a save, because they are carried on the record in `extras`.
//
// A Store serializes its own read-modify-write cycles with a mutex; the server
// keeps one Store per project, so two projects never contend.
package graph

import (
	"encoding/json"
	"strings"
)

// Entity is one node of the knowledge graph.
type Entity struct {
	Name         string   `json:"name"`
	EntityType   string   `json:"entityType"`
	Observations []string `json:"observations"`

	// extras holds members of a stored record that this model does not know,
	// so a save can write them back. It is unexported on purpose: encoding/json
	// never emits it, so tool results and read_graph never show it.
	//
	// A decoded record's extras map is never modified thereafter, so copies of an
	// Entity may share it; only encoding reads it.
	extras map[string]json.RawMessage
}

// Relation is one directed edge between two entities.
type Relation struct {
	From         string `json:"from"`
	To           string `json:"to"`
	RelationType string `json:"relationType"`

	// extras is the same immutable-after-decode map as Entity's.
	extras map[string]json.RawMessage
}

// KnowledgeGraph is the whole graph of one project.
type KnowledgeGraph struct {
	Entities  []Entity   `json:"entities"`
	Relations []Relation `json:"relations"`
}

// ReadEntity is an entity as a read operation returns it. ObservationCount is set
// only when the read was given an observation limit, so a count above
// len(Observations) tells the caller that observations were left out.
type ReadEntity struct {
	Name             string   `json:"name"`
	EntityType       string   `json:"entityType"`
	Observations     []string `json:"observations"`
	ObservationCount *int     `json:"observationCount,omitempty"`
}

// RelationView is a relation as a read operation returns it; it is the stored
// shape without the preserved extras, which never travel to a client.
// ReadGraph is the result of one read: the entities selected, and every relation
// that touches one of them.
type ReadGraph struct {
	Entities  []ReadEntity
	Relations []Relation
}

// ReadOptions narrows and limits a read. The zero value returns everything.
type ReadOptions struct {
	// EntityTypes keeps only entities whose type is exactly one of these. Empty
	// keeps every entity.
	EntityTypes []string
	// Limit caps how many entities are returned, in stored order. Zero returns
	// every match.
	Limit int
	// ObservationLimit caps each returned entity's observations, keeping the
	// first ones in the order they were added, and reports the total in
	// ObservationCount. Nil returns every observation.
	ObservationLimit *int
}

// CreateResult reports what a create_entities call added and what it skipped.
type CreateResult struct {
	Created []Entity
	Skipped []string
}

// ObservationAdd is one entity's requested observations.
type ObservationAdd struct {
	EntityName string
	Contents   []string
}

// ObservationAddResult reports the observations actually added to one entity.
type ObservationAddResult struct {
	EntityName        string
	AddedObservations []string
}

// DeleteEntitiesResult reports which names were removed and which were absent.
type DeleteEntitiesResult struct {
	Deleted  []string
	NotFound []string
}

// ObservationDelete is one entity's requested observation removals.
type ObservationDelete struct {
	EntityName   string
	Observations []string
}

// DeleteObservationsResult reports how many observations were removed and which
// entities were absent.
type DeleteObservationsResult struct {
	DeletedCount    int
	MissingEntities []string
}

var entityFields = map[string]bool{
	"type": true, "name": true, "entityType": true, "observations": true,
}

var relationFields = map[string]bool{
	"type": true, "from": true, "to": true, "relationType": true,
}

// unknownFields returns the members of fields that the model does not own. Object
// members are decoded into a map, so even a "__proto__" key stays data.
func unknownFields(fields map[string]json.RawMessage, known map[string]bool) map[string]json.RawMessage {
	out := make(map[string]json.RawMessage)
	for k, v := range fields {
		if !known[k] {
			out[k] = v
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// decodeLine turns one stored line into an entity or a relation. ok is false for
// anything the model cannot represent, which the caller then preserves verbatim.
func decodeLine(line string) (entity *Entity, relation *Relation, ok bool) {
	var probe struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(line), &probe); err != nil {
		return nil, nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(line), &fields); err != nil || fields == nil {
		return nil, nil, false
	}
	switch probe.Type {
	case "entity":
		var e Entity
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, nil, false
		}
		if e.Name == "" || e.EntityType == "" {
			return nil, nil, false
		}
		if e.Observations == nil {
			e.Observations = []string{}
		}
		e.extras = unknownFields(fields, entityFields)
		return &e, nil, true
	case "relation":
		var r Relation
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, nil, false
		}
		if r.From == "" || r.To == "" || r.RelationType == "" {
			return nil, nil, false
		}
		r.extras = unknownFields(fields, relationFields)
		return nil, &r, true
	}
	return nil, nil, false
}

// storedField is one modeled member of a stored line.
type storedField struct {
	name  string
	value any
}

// encodeRecord renders one stored line. Members are written through a map so the
// output is deterministic (keys are sorted) and preserved extras are carried
// over; modeled fields win over any stale extra of the same name.
func encodeRecord(extra map[string]json.RawMessage, modeled []storedField) ([]byte, error) {
	fields := make(map[string]json.RawMessage, len(extra)+len(modeled))
	for k, v := range extra {
		fields[k] = v
	}
	for _, m := range modeled {
		b, err := json.Marshal(m.value)
		if err != nil {
			return nil, err
		}
		fields[m.name] = b
	}
	return json.Marshal(fields)
}

// encodeEntity renders one entity as a stored line.
func encodeEntity(e Entity) ([]byte, error) {
	obs := e.Observations
	if obs == nil {
		obs = []string{}
	}
	return encodeRecord(e.extras, []storedField{
		{"type", "entity"},
		{"name", e.Name},
		{"entityType", e.EntityType},
		{"observations", obs},
	})
}

// encodeRelation renders one relation as a stored line, on the same terms as
// encodeEntity.
func encodeRelation(r Relation) ([]byte, error) {
	return encodeRecord(r.extras, []storedField{
		{"type", "relation"},
		{"from", r.From},
		{"to", r.To},
		{"relationType", r.RelationType},
	})
}

// cloneEntity copies an entity so a caller cannot mutate the stored graph. The
// copy's observations are always a non-nil slice: a tool result that says "null"
// where its published schema says "array" is a contract violation, and an empty
// observation list is a real, common state.
func cloneEntity(e Entity) Entity {
	c := e
	c.Observations = append([]string{}, e.Observations...)
	return c
}

func entityIndex(g *KnowledgeGraph, name string) int {
	for i := range g.Entities {
		if g.Entities[i].Name == name {
			return i
		}
	}
	return -1
}

func sameRelation(a, b Relation) bool {
	return a.From == b.From && a.To == b.To && a.RelationType == b.RelationType
}

func containsRelation(list []Relation, want Relation) bool {
	for _, existing := range list {
		if sameRelation(existing, want) {
			return true
		}
	}
	return false
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// matches reports whether an entity contains the already lowercased query in its
// name, type or observations. The caller folds the query once for the whole search.
func matches(e Entity, lowerQuery string) bool {
	if strings.Contains(strings.ToLower(e.Name), lowerQuery) ||
		strings.Contains(strings.ToLower(e.EntityType), lowerQuery) {
		return true
	}
	for _, o := range e.Observations {
		if strings.Contains(strings.ToLower(o), lowerQuery) {
			return true
		}
	}
	return false
}

// limitEntityObservations applies an observation limit to a set of entities.
func limitEntityObservations(entities []Entity, limit *int) []ReadEntity {
	out := make([]ReadEntity, 0, len(entities))
	for _, e := range entities {
		observations := e.Observations
		if observations == nil {
			observations = []string{}
		}
		view := ReadEntity{Name: e.Name, EntityType: e.EntityType, Observations: observations}
		if limit != nil {
			total := len(observations)
			if *limit >= 0 && *limit < total {
				view.Observations = append([]string(nil), observations[:*limit]...)
			}
			view.ObservationCount = &total
		}
		out = append(out, view)
	}
	return out
}

// keepEntityTypes filters entities by type. An empty list keeps everything.
func keepEntityTypes(entities []Entity, types []string) []Entity {
	if len(types) == 0 {
		return entities
	}
	want := make(map[string]bool, len(types))
	for _, t := range types {
		want[t] = true
	}
	out := make([]Entity, 0, len(entities))
	for _, e := range entities {
		if want[e.EntityType] {
			out = append(out, e)
		}
	}
	return out
}

// filterRelations keeps relations with at least one endpoint in names. The
// reference server widened this from "both endpoints" so a caller can discover
// a node's connections; the behavior is preserved.
func filterRelations(g KnowledgeGraph, names map[string]bool) []Relation {
	out := make([]Relation, 0)
	for _, r := range g.Relations {
		if names[r.From] || names[r.To] {
			out = append(out, r)
		}
	}
	return out
}
