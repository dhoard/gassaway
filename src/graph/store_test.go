// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for the JSONL store: the reference file format, the additive write
// semantics, the preservation rules, and the atomic save.
package graph_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/dhoard/gassaway/src/graph"
)

func newStore(t *testing.T) (*graph.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	return graph.NewStore(path), path
}

func TestReadEmptyWhenFileMissing(t *testing.T) {
	store, _ := newStore(t)
	g, err := store.Read()
	require.NoError(t, err)
	assert.Empty(t, g.Entities)
	assert.Empty(t, g.Relations)
}

func TestCreateAndReadRoundTrip(t *testing.T) {
	store, path := newStore(t)

	result, err := store.CreateEntities([]graph.Entity{
		{Name: "Ada", EntityType: "person", Observations: []string{"writes Go"}},
		{Name: "gassaway", EntityType: "project"},
	})
	require.NoError(t, err)
	assert.Len(t, result.Created, 2)
	assert.Empty(t, result.Skipped)

	_, err = store.CreateRelations([]graph.Relation{{From: "Ada", To: "gassaway", RelationType: "works_on"}})
	require.NoError(t, err)

	g, err := store.Read()
	require.NoError(t, err)
	assert.Len(t, g.Entities, 2)
	assert.Len(t, g.Relations, 1)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"type":"entity"`)
	assert.Contains(t, string(data), `"type":"relation"`)
}

func TestCreateEntitiesSkipsExistingAndRepeatedNames(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	result, err := store.CreateEntities([]graph.Entity{
		{Name: "Ada", EntityType: "person", Observations: []string{"lost"}},
		{Name: "Grace", EntityType: "person"},
		{Name: "Grace", EntityType: "person"},
	})
	require.NoError(t, err)
	assert.Equal(t, []string{"Ada", "Grace"}, result.Skipped)
	require.Len(t, result.Created, 1)

	g, err := store.Read()
	require.NoError(t, err)
	for _, e := range g.Entities {
		assert.NotContains(t, e.Observations, "lost", "a skipped entity's observations must not be added")
	}
}

func TestCreateRelationsRequiresBothEndpoints(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	_, err = store.CreateRelations([]graph.Relation{{From: "Ada", To: "nobody", RelationType: "knows"}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nobody")

	g, err := store.Read()
	require.NoError(t, err)
	assert.Empty(t, g.Relations, "a failed call must not write a partial relation")
}

func TestDuplicateRelationsSkipped(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "a", EntityType: "t"}, {Name: "b", EntityType: "t"}})
	require.NoError(t, err)

	created, err := store.CreateRelations([]graph.Relation{
		{From: "a", To: "b", RelationType: "r"},
		{From: "a", To: "b", RelationType: "r"},
	})
	require.NoError(t, err)
	assert.Len(t, created, 1)
}

func TestAddObservationsDeduplicates(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person", Observations: []string{"one"}}})
	require.NoError(t, err)

	results, err := store.AddObservations([]graph.ObservationAdd{
		{EntityName: "Ada", Contents: []string{"one", "two", "two"}},
	})
	require.NoError(t, err)
	require.Len(t, results, 1)
	assert.Equal(t, []string{"two"}, results[0].AddedObservations)

	_, err = store.AddObservations([]graph.ObservationAdd{{EntityName: "nobody", Contents: []string{"x"}}})
	assert.Error(t, err)
}

func TestDeleteEntitiesCascadesRelations(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "a", EntityType: "t"}, {Name: "b", EntityType: "t"}})
	require.NoError(t, err)
	_, err = store.CreateRelations([]graph.Relation{{From: "a", To: "b", RelationType: "r"}})
	require.NoError(t, err)

	result, err := store.DeleteEntities([]string{"a", "missing"})
	require.NoError(t, err)
	assert.Equal(t, []string{"a"}, result.Deleted)
	assert.Equal(t, []string{"missing"}, result.NotFound)

	g, err := store.Read()
	require.NoError(t, err)
	assert.Len(t, g.Entities, 1)
	assert.Empty(t, g.Relations)
}

func TestDeleteObservationsReportsMissing(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person", Observations: []string{"one", "two"}}})
	require.NoError(t, err)

	result, err := store.DeleteObservations([]graph.ObservationDelete{
		{EntityName: "Ada", Observations: []string{"two", "absent"}},
		{EntityName: "nobody", Observations: []string{"x"}},
	})
	require.NoError(t, err)
	assert.Equal(t, 1, result.DeletedCount)
	assert.Equal(t, []string{"nobody"}, result.MissingEntities)
}

func TestDeleteRelations(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "a", EntityType: "t"}, {Name: "b", EntityType: "t"}})
	require.NoError(t, err)
	_, err = store.CreateRelations([]graph.Relation{{From: "a", To: "b", RelationType: "r"}})
	require.NoError(t, err)

	deleted, err := store.DeleteRelations([]graph.Relation{{From: "a", To: "b", RelationType: "r"}})
	require.NoError(t, err)
	assert.Equal(t, 1, deleted)
}

func TestSearchAndOpenIncludeTouchingRelations(t *testing.T) {
	store, _ := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{
		{Name: "Ada", EntityType: "person", Observations: []string{"writes Go"}},
		{Name: "gassaway", EntityType: "project"},
	})
	require.NoError(t, err)
	_, err = store.CreateRelations([]graph.Relation{{From: "Ada", To: "gassaway", RelationType: "works_on"}})
	require.NoError(t, err)

	found, err := store.SearchNodes("go", graph.ReadOptions{})
	require.NoError(t, err)
	assert.Len(t, found.Entities, 1)
	assert.Len(t, found.Relations, 1, "a relation touching a match is included")

	opened, err := store.OpenNodes([]string{"gassaway"}, graph.ReadOptions{})
	require.NoError(t, err)
	require.Len(t, opened.Entities, 1)
	assert.Len(t, opened.Relations, 1)
}

func TestUnreadableLinesSurviveWrites(t *testing.T) {
	store, path := newStore(t)
	require.NoError(t, os.WriteFile(path, []byte(
		`{"type":"entity","name":"Ada","entityType":"person","observations":[]}`+"\n"+
			`not json at all`+"\n"+
			`{"type":"entity","name":"","entityType":"person","observations":[]}`+"\n",
	), 0o644))

	g, err := store.Read()
	require.NoError(t, err)
	require.Len(t, g.Entities, 1, "only the readable entity is served")

	_, err = store.CreateEntities([]graph.Entity{{Name: "Grace", EntityType: "person"}})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "not json at all", "an unreadable line must be written back unchanged")
	assert.Contains(t, string(data), `"name":""`, "an invalid record must be written back unchanged")
}

func TestUnknownFieldsSurviveWrites(t *testing.T) {
	store, path := newStore(t)
	require.NoError(t, os.WriteFile(path, []byte(
		`{"type":"entity","name":"Ada","entityType":"person","observations":[],"createdAt":"2024-01-01"}`+"\n",
	), 0o644))

	_, err := store.CreateEntities([]graph.Entity{{Name: "Grace", EntityType: "person"}})
	require.NoError(t, err)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), `"createdAt":"2024-01-01"`, "an unrelated write must not drop fields the model does not own")
}

func TestSaveKeepsFileMode(t *testing.T) {
	store, path := newStore(t)
	require.NoError(t, os.WriteFile(path, []byte(""), 0o600))

	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm())
}

func TestSaveFailureLeavesThePreviousGraphIntact(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission-based failure injection does not apply to root")
	}
	store, path := newStore(t)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	// A directory that cannot hold the temporary file fails the save at its first
	// step, so the published file must still be the graph from before the call.
	dir := filepath.Dir(path)
	require.NoError(t, os.Chmod(dir, 0o500))
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	_, err = store.CreateEntities([]graph.Entity{{Name: "Grace", EntityType: "person"}})
	require.Error(t, err)

	require.NoError(t, os.Chmod(dir, 0o700))
	g, err := store.Read()
	require.NoError(t, err)
	require.Len(t, g.Entities, 1)
	assert.Equal(t, "Ada", g.Entities[0].Name)
}

// Two Store values over one path must exclude each other: the server can drop a
// project's runtime while a client session still holds the old one, and separate
// mutexes would let their read-modify-write cycles interleave and lose updates.
func TestTwoStoresOnOnePathSerialize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	stores := []*graph.Store{graph.NewStore(path), graph.NewStore(path)}

	const each = 25
	var wg sync.WaitGroup
	errs := make([]error, len(stores))
	for i, store := range stores {
		wg.Add(1)
		go func(i int, store *graph.Store) {
			defer wg.Done()
			for n := 0; n < each; n++ {
				name := fmt.Sprintf("s%d-e%d", i, n)
				if _, err := store.CreateEntities([]graph.Entity{{Name: name, EntityType: "t"}}); err != nil {
					errs[i] = err
					return
				}
			}
		}(i, store)
	}
	wg.Wait()
	for i, err := range errs {
		require.NoError(t, err, "store %d", i)
	}

	g, err := graph.NewStore(path).Read()
	require.NoError(t, err)
	assert.Len(t, g.Entities, len(stores)*each, "no write may be lost between two stores over one path")
}

// The prepare hook runs once, before the first save, and nothing exists on disk
// until then: a read of a project that was never written leaves no directory.
func TestPrepareRunsOnceBeforeTheFirstSave(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "project")
	path := filepath.Join(dir, "memory.jsonl")
	store := graph.NewStore(path)
	created := 0
	store.SetPrepare(func() error {
		created++
		return os.MkdirAll(dir, 0o755)
	})

	_, err := store.Read()
	require.NoError(t, err)
	_, statErr := os.Stat(dir)
	assert.True(t, os.IsNotExist(statErr), "a read must not create the project directory")

	_, err = store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)
	assert.Equal(t, 1, created, "the prepare hook runs before the first save")

	_, err = store.CreateEntities([]graph.Entity{{Name: "Grace", EntityType: "person"}})
	require.NoError(t, err)
	assert.Equal(t, 1, created, "the prepare hook runs only once")
}

// A prepare failure is a failed save, and the previous graph is untouched.
func TestPrepareFailureFailsTheSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memory.jsonl")
	store := graph.NewStore(path)
	_, err := store.CreateEntities([]graph.Entity{{Name: "Ada", EntityType: "person"}})
	require.NoError(t, err)

	store.SetPrepare(func() error { return errors.New("no directory") })
	_, err = store.CreateEntities([]graph.Entity{{Name: "Grace", EntityType: "person"}})
	require.Error(t, err)

	g, err := store.Read()
	require.NoError(t, err)
	require.Len(t, g.Entities, 1)
	assert.Equal(t, "Ada", g.Entities[0].Name)
}
