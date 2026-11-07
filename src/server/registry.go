// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file owns the per-project runtime: the store, the MCP server bound to it,
// and the registry that creates each one on first use.
//
// One project gets one *mcp.Server and one store. That is what makes two Amanda
// instances in the same project share a graph while two projects stay
// independent: the SDK may hand the same *mcp.Server to many sessions, and the
// store's mutex serializes their writes.
package server

import (
	"bytes"
	"container/list"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/graph"
	"github.com/dhoard/gassaway/src/memory"
	"github.com/dhoard/gassaway/src/projects"
)

// Project is one project's runtime state.
type Project struct {
	ID     string
	Store  *graph.Store
	Server *mcp.Server
}

// Registry keeps the runtime of recently used projects, most recently used
// first, and drops the least recently used runtime when the cap is reached.
//
// Dropping a runtime is safe because a runtime holds no exclusive resource: the
// graph's write lock belongs to the graph package and is keyed by path, so the
// Store of a dropped project and the Store of its replacement exclude each other
// (see graph.lockFor). Sessions that were connected to a dropped runtime keep
// working: they hold the old *mcp.Server, which reads and writes the same file
// through the same lock.
type Registry struct {
	root string
	max  int
	log  *slog.Logger

	mu       sync.Mutex
	projects map[string]*list.Element // element.Value is *Project
	order    *list.List
}

// NewRegistry returns a registry whose projects live under root. At most max
// project runtimes are kept; the least recently used is dropped when the bound is
// reached and rebuilt on the next request. A nil logger discards everything,
// which is what the tests that assert behaviour rather than logging use.
func NewRegistry(root string, max int, log *slog.Logger) *Registry {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	if max < 1 {
		max = 1
	}
	return &Registry{
		root:     root,
		max:      max,
		log:      log,
		projects: make(map[string]*list.Element),
		order:    list.New(),
	}
}

// Get returns the project for ID, creating its runtime on first use. Nothing is
// written to disk here: a request that only reads a project leaves it exactly as
// it found it, and the directory and its metadata appear when there is something
// to store (see graph.Store.SetPrepare).
//
// The id is validated here as well as at the HTTP edge: this is the only function
// that turns a name into a filesystem path, so it is the place the invariant has
// to hold.
func (r *Registry) Get(id string) (*Project, error) {
	if err := projects.Validate(id); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if element, ok := r.projects[id]; ok {
		r.order.MoveToFront(element)
		return element.Value.(*Project), nil
	}

	graphPath, err := projects.GraphPath(r.root, id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(graphPath)

	// An existing project is validated when it is opened; a nonexistent one is
	// not created. Any error other than "not there" is reported rather than read
	// as "not there", so a project whose metadata cannot be checked does not
	// silently become an empty graph.
	if _, err := os.Stat(dir); err == nil {
		if err := validateMetadata(dir, id); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspecting project directory %s: %w", dir, err)
	}

	p := r.newProject(id, graphPath)
	r.projects[id] = r.order.PushFront(p)
	r.evictIfNeeded()
	return p, nil
}

// newProject builds one project's runtime. The store is given a prepare hook so
// that the project directory and its metadata are published by the first save,
// atomically and only when there is something to store.
func (r *Registry) newProject(id, graphPath string) *Project {
	dir := filepath.Dir(graphPath)
	store := graph.NewStore(graphPath)
	store.SetPrepare(func() error { return ensureMetadata(dir, id) })

	srv := mcp.NewServer(&mcp.Implementation{Name: "gassaway", Version: buildinfo.Version}, &mcp.ServerOptions{
		Instructions: "A knowledge graph of entities, relations and observations for one project.",
		Logger:       r.log,
		// A session belongs to the project whose server minted it. Prefixing the
		// project ID makes that ownership checkable at the HTTP edge without a
		// side table, and the random suffix is still what makes the ID
		// unguessable. See router.go withProject.
		GetSessionID: func() string { return id + "." + rand.Text() },
	})
	// Installed before the tools, so every method this project serves runs inside
	// them. The logger is outermost so a tool error is recorded with the tool
	// name; the recovery wrapper is inside it and is what turns a panic into an
	// error (a panic unwinds past the logger without a result to record).
	srv.AddReceivingMiddleware(toolCallLogger(id, r.log), recoverPanics(id, r.log))
	memory.Register(srv, store)
	memory.RegisterResource(srv, store)

	return &Project{ID: id, Store: store, Server: srv}
}

// evictIfNeeded drops the least recently used runtime while the registry is over
// its cap. It is called with mu held.
func (r *Registry) evictIfNeeded() {
	for len(r.projects) > r.max {
		oldest := r.order.Back()
		if oldest == nil {
			return
		}
		p := oldest.Value.(*Project)
		r.order.Remove(oldest)
		delete(r.projects, p.ID)
		r.log.Debug("evicted project runtime", "project", p.ID, "open", len(r.projects))
	}
}

// Count reports how many project directories the root holds, read from disk so it
// includes projects this process has not opened. A root that does not exist yet is
// an empty one, not an error.
func (r *Registry) Count() (int, error) {
	entries, err := os.ReadDir(r.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := projects.Dir(r.root, entry.Name()); err != nil {
			continue
		}
		count++
	}
	return count, nil
}

// validateMetadata checks an existing project's metadata. A mismatched or corrupt
// document is never silently replaced. A directory that already holds data but no
// metadata document gets one written, which is what keeps a hand-made directory
// usable without letting a corrupt document pass.
func validateMetadata(dir, id string) error {
	return ensureMetadata(dir, id)
}

// ensureMetadata validates the project's metadata document and creates it when
// the project has none, atomically. It is the store's prepare hook, so it runs
// when the project is about to hold data for the first time.
func ensureMetadata(dir, id string) error {
	type metadata struct {
		SchemaVersion int    `json:"schemaVersion"`
		ProjectID     string `json:"projectId"`
	}
	file := filepath.Join(dir, "project.json")
	expected := metadata{SchemaVersion: 1, ProjectID: id}
	data, err := os.ReadFile(file)
	if err == nil {
		var actual metadata
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&actual) != nil || decoder.Decode(new(any)) != io.EOF || actual != expected {
			return fmt.Errorf("project metadata does not match its ID: %s", file)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("reading project metadata: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating project directory %s: %w", dir, err)
	}
	data, err = json.Marshal(expected)
	if err != nil {
		return err
	}
	if err := graph.PublishFile(file, ".project-*.json", append(data, '\n'), 0); err != nil {
		return fmt.Errorf("publishing project metadata: %w", err)
	}
	return nil
}
