// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file is the storage engine: loading the JSONL file, saving it
// atomically, and the nine graph operations the memory tools expose.
//
// Saving writes a temporary file in the same directory, syncs it, renames it
// over the target, and then syncs the directory. write-then-rename is atomic on
// POSIX filesystems, so a reader sees either the complete old graph or the
// complete new one, never a truncated file. The rename replaces the inode, so
// the existing file's mode is copied onto the temporary file first; otherwise an
// operator's 0600 would come back 0644.
//
// Syncing the file alone flushes contents but not the rename, which is a change
// to the directory's entries; without the second sync a crash or power loss can
// leave the old name in place and silently discard an acknowledged write. The
// store therefore reports a save as failed if it cannot make the rename durable.
package graph

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

// Store is one project's graph file. Every read-modify-write cycle is serialized
// by a lock keyed by the file's path; reads alone do not take it, because the
// file is replaced atomically.
type Store struct {
	path string

	// lockKey names this store's write lock in the process-wide table (see
	// acquirePath). The server may drop and later recreate a project's runtime —
	// for example when an LRU cache evicts it while a client session still holds
	// the old one — and two Stores with independent mutexes would let two
	// read-modify-write cycles interleave and lose an update. Keying the lock by
	// path makes that impossible regardless of how many Store values exist.
	lockKey string

	// prepare, when set, runs before every save, under the write lock. The owner
	// of the file uses it to create the project directory and its metadata at the
	// moment there is something to store, so a request that only reads a project
	// leaves no trace on disk. It must be idempotent: it runs again on each save,
	// which is what brings back a directory an operator removed while the server
	// was running.
	prepare func() error
}

// NewStore returns a store over the JSONL file at path. Nothing is created here:
// the directory is made by a save, either directly or through the prepare hook.
func NewStore(path string) *Store {
	key := path
	if abs, err := filepath.Abs(path); err == nil {
		key = abs
	}
	return &Store{path: path, lockKey: key}
}

// SetPrepare registers fn to run before each of this store's saves. A nil fn
// restores the default behaviour of creating the directory with MkdirAll. It is
// called by the owner of the project while the store is still private to it, so
// it is not safe to call concurrently with a save.
func (s *Store) SetPrepare(fn func() error) { s.prepare = fn }

// pathLock is one path's write lock and the number of goroutines that hold or
// wait for it.
type pathLock struct {
	mu   sync.Mutex
	refs int
}

// pathLocks holds a lock only for paths with a write in flight. An entry is
// created by the first writer to arrive and removed when the last one leaves, so
// the table is bounded by the number of concurrent writes and not by the number
// of project IDs the process has ever been asked about: a request that names a
// project and never writes to it leaves nothing behind.
var (
	pathLocksMu sync.Mutex
	pathLocks   = make(map[string]*pathLock)
)

// acquirePath takes the write lock for key and returns the function that
// releases it. Every writer over one path shares one entry for as long as any of
// them holds or waits for it, which is what makes two Stores over the same file
// exclude each other.
func acquirePath(key string) (release func()) {
	pathLocksMu.Lock()
	l, ok := pathLocks[key]
	if !ok {
		l = new(pathLock)
		pathLocks[key] = l
	}
	l.refs++
	pathLocksMu.Unlock()

	l.mu.Lock()
	return func() {
		l.mu.Unlock()
		pathLocksMu.Lock()
		l.refs--
		if l.refs == 0 {
			delete(pathLocks, key)
		}
		pathLocksMu.Unlock()
	}
}

// heldPathLocks reports how many paths currently have a lock entry. It exists
// for the test that guards the table against growing without bound.
func heldPathLocks() int {
	pathLocksMu.Lock()
	defer pathLocksMu.Unlock()
	return len(pathLocks)
}

// ensureDir makes the graph's directory exist, through the prepare hook when one
// is set. It is called with the write lock held, before every save.
func (s *Store) ensureDir() error {
	if s.prepare == nil {
		return os.MkdirAll(filepath.Dir(s.path), 0o755)
	}
	return s.prepare()
}

// snapshot is one reading of the file: the graph it holds and the lines the model
// could not read, which the next save writes back.
type snapshot struct {
	graph      KnowledgeGraph
	unreadable []string
}

// load reads the file. A missing file is an empty graph, which is how a project
// starts; any other error is returned rather than read as "empty".
func (s *Store) load() (snapshot, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return snapshot{graph: KnowledgeGraph{Entities: []Entity{}, Relations: []Relation{}}}, nil
		}
		return snapshot{}, err
	}

	g, unreadable := decodeLines(data)
	return snapshot{graph: g, unreadable: unreadable}, nil
}

// decodeLines turns file contents into a graph and the lines the model cannot
// represent. It is the only reader of the stored format: load keeps the
// unreadable lines so the next save can write them back rather than dropping a
// line it does not understand.
func decodeLines(data []byte) (KnowledgeGraph, []string) {
	out := KnowledgeGraph{Entities: []Entity{}, Relations: []Relation{}}
	var unreadable []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		entity, relation, ok := decodeLine(line)
		if !ok {
			unreadable = append(unreadable, line)
			continue
		}
		if entity != nil {
			out.Entities = append(out.Entities, *entity)
		} else {
			out.Relations = append(out.Relations, *relation)
		}
	}
	return out, unreadable
}

// save writes the graph and then the lines the model could not read, preserving
// them byte for byte.
func (s *Store) save(l snapshot) error {
	lines := make([]string, 0, len(l.graph.Entities)+len(l.graph.Relations)+len(l.unreadable))
	for _, e := range l.graph.Entities {
		b, err := encodeEntity(e)
		if err != nil {
			return fmt.Errorf("encoding entity: %w", err)
		}
		lines = append(lines, string(b))
	}
	for _, r := range l.graph.Relations {
		b, err := encodeRelation(r)
		if err != nil {
			return fmt.Errorf("encoding relation: %w", err)
		}
		lines = append(lines, string(b))
	}
	lines = append(lines, l.unreadable...)

	if err := s.ensureDir(); err != nil {
		return err
	}

	mode := fs.FileMode(0)
	existing, statErr := os.Stat(s.path)
	switch {
	case statErr == nil:
		mode = existing.Mode().Perm()
	case errors.Is(statErr, fs.ErrNotExist):
		// A new graph: the umask decides, as it would for any new file.
	default:
		return statErr
	}

	data := []byte(strings.Join(lines, "\n") + "\n")
	return PublishFile(s.path, filepath.Base(s.path)+".*.tmp", data, mode)
}

// PublishFile writes data to a temporary file beside path, makes it durable,
// renames it over path, and syncs the directory, so a reader sees either the
// complete old file or the complete new one and never a truncated file.
// tempPattern is the os.CreateTemp pattern for the temporary file; mode 0 leaves
// the temporary file's own 0600 in place.
//
// It is the one writer for everything the server must publish atomically: the
// graph file and the project metadata. Errors are returned exactly as the failed
// operation reported them, so a caller that wants to name its own stage adds the
// wording itself.
func PublishFile(path, tempPattern string, data []byte, mode fs.FileMode) error {
	temp, err := os.CreateTemp(filepath.Dir(path), tempPattern)
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}
	if mode != 0 {
		if err := temp.Chmod(mode); err != nil {
			cleanup()
			return err
		}
	}
	if _, err := temp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return syncDir(filepath.Dir(path))
}

// syncDir flushes a directory's entries, making a rename inside it durable.
//
// A filesystem that cannot sync a directory reports EINVAL or ENOTSUP; that is a
// statement about the filesystem, not about this write, so it is tolerated. Any
// other error is returned, because a save whose rename may not survive a crash
// must not be reported as successful. The rename has already taken place by
// then, so such a failure means the new graph may be visible without being
// durable; that is still the honest answer, since the caller cannot rely on it.
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	err = d.Sync()
	if err == nil || errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	return err
}

// Mutate runs fn over a freshly loaded graph and saves the result, holding the
// path's write lock across the whole cycle so concurrent callers cannot lose each
// other's writes. An error from fn leaves the file untouched.
func (s *Store) Mutate(fn func(*KnowledgeGraph) error) error {
	defer acquirePath(s.lockKey)()

	l, err := s.load()
	if err != nil {
		return err
	}
	if err := fn(&l.graph); err != nil {
		return err
	}
	return s.save(l)
}

// Read returns the whole graph.
func (s *Store) Read() (KnowledgeGraph, error) {
	l, err := s.load()
	if err != nil {
		return KnowledgeGraph{}, err
	}
	return l.graph, nil
}

// graphFrom selects the part of a loaded graph a read asked for: the entities
// the filter kept, capped by the options, with the relations that touch them.
func graphFrom(g KnowledgeGraph, entities []Entity, opts ReadOptions) ReadGraph {
	if opts.Limit > 0 && len(entities) > opts.Limit {
		entities = entities[:opts.Limit]
	}
	names := make(map[string]bool, len(entities))
	for _, e := range entities {
		names[e.Name] = true
	}
	return ReadGraph{
		Entities:  limitEntityObservations(entities, opts.ObservationLimit),
		Relations: filterRelations(g, names),
	}
}

// ReadGraph returns the whole graph, optionally filtered by entity type and
// capped by opts.
func (s *Store) ReadGraph(opts ReadOptions) (ReadGraph, error) {
	g, err := s.Read()
	if err != nil {
		return ReadGraph{}, err
	}
	return graphFrom(g, keepEntityTypes(g.Entities, opts.EntityTypes), opts), nil
}

// CreateEntities adds entities whose names are new, skipping names that already
// exist and names repeated earlier in the same call. Skipped entities keep none
// of their observations, which is why the result reports them. An entity without
// a name or a type fails the whole call and nothing is written: the stored format
// cannot represent one, so it would be acknowledged and then never read back.
func (s *Store) CreateEntities(entities []Entity) (CreateResult, error) {
	for i, e := range entities {
		if e.Name == "" {
			return CreateResult{}, &InvalidRecordError{Kind: "entity", Index: i, Field: "name"}
		}
		if e.EntityType == "" {
			return CreateResult{}, &InvalidRecordError{Kind: "entity", Index: i, Field: "entityType"}
		}
	}
	result := CreateResult{Created: []Entity{}, Skipped: []string{}}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		taken := entityNames(g)
		for _, e := range entities {
			if taken[e.Name] {
				result.Skipped = append(result.Skipped, e.Name)
				continue
			}
			taken[e.Name] = true
			created := cloneEntity(e)
			g.Entities = append(g.Entities, created)
			result.Created = append(result.Created, cloneEntity(created))
		}
		return nil
	})
	if err != nil {
		return CreateResult{}, err
	}
	return result, nil
}

// CreateRelations adds relations that are not already present. Every endpoint
// must exist and every relation must name its type, and the whole call fails if
// one does not.
func (s *Store) CreateRelations(relations []Relation) ([]Relation, error) {
	for i, r := range relations {
		switch {
		case r.From == "":
			return nil, &InvalidRecordError{Kind: "relation", Index: i, Field: "from"}
		case r.To == "":
			return nil, &InvalidRecordError{Kind: "relation", Index: i, Field: "to"}
		case r.RelationType == "":
			return nil, &InvalidRecordError{Kind: "relation", Index: i, Field: "relationType"}
		}
	}
	created := []Relation{}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		names := entityNames(g)
		for _, r := range relations {
			if !names[r.From] {
				return &EntityNotFoundError{Name: r.From}
			}
			if !names[r.To] {
				return &EntityNotFoundError{Name: r.To}
			}
		}
		// The set starts as the stored relations and takes each one added here, so
		// one lookup dedupes against both the stored graph and this batch.
		present := relationKeys(g.Relations)
		for _, r := range relations {
			k := keyOf(r)
			if present[k] {
				continue
			}
			present[k] = true
			g.Relations = append(g.Relations, r)
			created = append(created, r)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return created, nil
}

// AddObservations extends existing entities, skipping observation strings an
// entity already has. A missing entity fails the whole call.
func (s *Store) AddObservations(adds []ObservationAdd) ([]ObservationAddResult, error) {
	results := make([]ObservationAddResult, 0, len(adds))
	err := s.Mutate(func(g *KnowledgeGraph) error {
		index := entityIndexes(g)
		// One set per entity touched, built on first use and kept for the rest of
		// the call, so an entity named twice still dedupes across both items.
		held := make(map[int]map[string]bool)
		for _, add := range adds {
			i, ok := index[add.EntityName]
			if !ok {
				return &EntityNotFoundError{Name: add.EntityName}
			}
			have := held[i]
			if have == nil {
				have = stringSet(g.Entities[i].Observations)
				held[i] = have
			}
			added := []string{}
			for _, content := range add.Contents {
				if have[content] {
					continue
				}
				have[content] = true
				g.Entities[i].Observations = append(g.Entities[i].Observations, content)
				added = append(added, content)
			}
			results = append(results, ObservationAddResult{
				EntityName:        add.EntityName,
				AddedObservations: added,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}

// DeleteEntities removes entities and every relation that touches them.
func (s *Store) DeleteEntities(names []string) (DeleteEntitiesResult, error) {
	result := DeleteEntitiesResult{Deleted: []string{}, NotFound: []string{}}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		want := stringSet(names)
		present := entityNames(g)
		for _, n := range names {
			if present[n] {
				result.Deleted = append(result.Deleted, n)
			} else {
				result.NotFound = append(result.NotFound, n)
			}
		}
		entities := make([]Entity, 0, len(g.Entities))
		for _, e := range g.Entities {
			if !want[e.Name] {
				entities = append(entities, e)
			}
		}
		relations := make([]Relation, 0, len(g.Relations))
		for _, r := range g.Relations {
			if !want[r.From] && !want[r.To] {
				relations = append(relations, r)
			}
		}
		g.Entities, g.Relations = entities, relations
		return nil
	})
	if err != nil {
		return DeleteEntitiesResult{}, err
	}
	return result, nil
}

// DeleteObservations removes named observations. A missing entity is reported,
// not treated as a failure.
func (s *Store) DeleteObservations(deletes []ObservationDelete) (DeleteObservationsResult, error) {
	result := DeleteObservationsResult{MissingEntities: []string{}}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		index := entityIndexes(g)
		for _, d := range deletes {
			i, ok := index[d.EntityName]
			if !ok {
				result.MissingEntities = append(result.MissingEntities, d.EntityName)
				continue
			}
			remove := stringSet(d.Observations)
			kept := make([]string, 0, len(g.Entities[i].Observations))
			for _, o := range g.Entities[i].Observations {
				if remove[o] {
					result.DeletedCount++
					continue
				}
				kept = append(kept, o)
			}
			g.Entities[i].Observations = kept
		}
		return nil
	})
	if err != nil {
		return DeleteObservationsResult{}, err
	}
	return result, nil
}

// DeleteRelations removes relations matching all three of from, to and type.
func (s *Store) DeleteRelations(relations []Relation) (int, error) {
	deleted := 0
	err := s.Mutate(func(g *KnowledgeGraph) error {
		remove := relationKeys(relations)
		kept := make([]Relation, 0, len(g.Relations))
		for _, r := range g.Relations {
			if remove[keyOf(r)] {
				deleted++
				continue
			}
			kept = append(kept, r)
		}
		g.Relations = kept
		return nil
	})
	if err != nil {
		return 0, err
	}
	return deleted, nil
}

// SearchNodes returns entities matching query, capped by opts, and every relation
// that touches one of them.
func (s *Store) SearchNodes(query string, opts ReadOptions) (ReadGraph, error) {
	g, err := s.Read()
	if err != nil {
		return ReadGraph{}, err
	}
	lowerQuery := strings.ToLower(query)
	matched := make([]Entity, 0, len(g.Entities))
	for _, e := range g.Entities {
		if matches(e, lowerQuery) {
			matched = append(matched, e)
		}
	}
	return graphFrom(g, matched, opts), nil
}

// OpenNodes returns the named entities, capped by opts, and every relation that
// touches one of them.
func (s *Store) OpenNodes(names []string, opts ReadOptions) (ReadGraph, error) {
	g, err := s.Read()
	if err != nil {
		return ReadGraph{}, err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	matched := make([]Entity, 0, len(names))
	for _, e := range g.Entities {
		if want[e.Name] {
			matched = append(matched, e)
		}
	}
	return graphFrom(g, matched, opts), nil
}
