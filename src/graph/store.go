// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
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

// Store is one project's graph file. Its mutex serializes every read-modify-write
// cycle; reads alone do not take it, because the file is replaced atomically.
type Store struct {
	path string
	mu   sync.Mutex
}

// NewStore returns a store over the JSONL file at path. The directory is not
// created here: the owner of the project (the server registry) does that.
func NewStore(path string) *Store {
	return &Store{path: path}
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
			return fmt.Errorf("encoding entity %q: %w", e.Name, err)
		}
		lines = append(lines, string(b))
	}
	for _, r := range l.graph.Relations {
		b, err := encodeRelation(r)
		if err != nil {
			return fmt.Errorf("encoding relation %q->%q: %w", r.From, r.To, err)
		}
		lines = append(lines, string(b))
	}
	lines = append(lines, l.unreadable...)

	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
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
// store's mutex across the whole cycle so concurrent callers cannot lose each
// other's writes. An error from fn leaves the file untouched.
func (s *Store) Mutate(fn func(*KnowledgeGraph) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

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

// CreateEntities adds entities whose names are new, skipping names that already
// exist and names repeated earlier in the same call. Skipped entities keep none
// of their observations, which is why the result reports them.
func (s *Store) CreateEntities(entities []Entity) (CreateResult, error) {
	result := CreateResult{Created: []Entity{}, Skipped: []string{}}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		seen := make(map[string]bool, len(entities))
		for _, e := range entities {
			if seen[e.Name] || entityIndex(g, e.Name) >= 0 {
				result.Skipped = append(result.Skipped, e.Name)
				continue
			}
			seen[e.Name] = true
			created := cloneEntity(e)
			if created.Observations == nil {
				created.Observations = []string{}
			}
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
// must exist, and the whole call fails if one does not.
func (s *Store) CreateRelations(relations []Relation) ([]Relation, error) {
	created := []Relation{}
	err := s.Mutate(func(g *KnowledgeGraph) error {
		for _, r := range relations {
			if entityIndex(g, r.From) < 0 {
				return fmt.Errorf("entity with name %s not found", r.From)
			}
			if entityIndex(g, r.To) < 0 {
				return fmt.Errorf("entity with name %s not found", r.To)
			}
		}
		for _, r := range relations {
			// g.Relations already holds the relations added earlier in this call, so
			// one scan dedupes against both the stored graph and this batch.
			if containsRelation(g.Relations, r) {
				continue
			}
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
		for _, add := range adds {
			i := entityIndex(g, add.EntityName)
			if i < 0 {
				return fmt.Errorf("entity with name %s not found", add.EntityName)
			}
			added := []string{}
			for _, content := range add.Contents {
				if containsString(g.Entities[i].Observations, content) || containsString(added, content) {
					continue
				}
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
		want := make(map[string]bool, len(names))
		for _, n := range names {
			want[n] = true
		}
		for _, n := range names {
			if entityIndex(g, n) >= 0 {
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
		for _, d := range deletes {
			i := entityIndex(g, d.EntityName)
			if i < 0 {
				result.MissingEntities = append(result.MissingEntities, d.EntityName)
				continue
			}
			kept := make([]string, 0, len(g.Entities[i].Observations))
			for _, o := range g.Entities[i].Observations {
				if containsString(d.Observations, o) {
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
		kept := make([]Relation, 0, len(g.Relations))
		for _, r := range g.Relations {
			remove := false
			for _, d := range relations {
				if sameRelation(r, d) {
					remove = true
					break
				}
			}
			if remove {
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

// SearchNodes returns entities matching query and every relation touching them.
func (s *Store) SearchNodes(query string) (KnowledgeGraph, error) {
	g, err := s.Read()
	if err != nil {
		return KnowledgeGraph{}, err
	}
	names := make(map[string]bool)
	out := KnowledgeGraph{Entities: []Entity{}, Relations: []Relation{}}
	lowerQuery := strings.ToLower(query)
	for _, e := range g.Entities {
		if matches(e, lowerQuery) {
			out.Entities = append(out.Entities, cloneEntity(e))
			names[e.Name] = true
		}
	}
	out.Relations = filterRelations(g, names)
	return out, nil
}

// OpenNodes returns the named entities and every relation touching them.
func (s *Store) OpenNodes(names []string) (KnowledgeGraph, error) {
	g, err := s.Read()
	if err != nil {
		return KnowledgeGraph{}, err
	}
	want := make(map[string]bool, len(names))
	for _, n := range names {
		want[n] = true
	}
	out := KnowledgeGraph{Entities: []Entity{}, Relations: []Relation{}}
	for _, e := range g.Entities {
		if want[e.Name] {
			out.Entities = append(out.Entities, cloneEntity(e))
		}
	}
	out.Relations = filterRelations(g, want)
	return out, nil
}
