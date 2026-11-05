// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/graph"
	"github.com/dhoard/gassaway/src/memory"
	"github.com/dhoard/gassaway/src/projects"
)

// ErrTooManyProjects is returned when opening a new project would exceed the
// configured cap. The HTTP layer maps it to 429.
var ErrTooManyProjects = errors.New("too many projects")

// Project is one project's runtime state.
type Project struct {
	ID     string
	Store  *graph.Store
	Server *mcp.Server
}

// Registry memoizes projects by ID.
type Registry struct {
	root string
	max  int

	mu       sync.Mutex
	projects map[string]*Project
}

// NewRegistry returns a registry whose projects live under root, holding at most
// max open projects.
func NewRegistry(root string, max int) *Registry {
	return &Registry{root: root, max: max, projects: make(map[string]*Project)}
}

// Get returns the project for ID, creating its directory and runtime on first
// use. The id is validated here as well as at the HTTP edge: this is the only
// function that turns a name into a filesystem path, so it is the place the
// invariant has to hold.
func (r *Registry) Get(id string) (*Project, error) {
	if err := projects.Validate(id); err != nil {
		return nil, err
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if p, ok := r.projects[id]; ok {
		return p, nil
	}
	if len(r.projects) >= r.max {
		return nil, fmt.Errorf("%w: %d projects are already open (see %s)", ErrTooManyProjects, len(r.projects), config.EnvMaxProjects)
	}

	graphPath, err := projects.GraphPath(r.root, id)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(graphPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("creating project directory %s: %w", dir, err)
	}

	if err := ensureMetadata(dir, id); err != nil {
		return nil, err
	}

	store := graph.NewStore(graphPath)
	srv := mcp.NewServer(&mcp.Implementation{Name: "gassaway", Version: buildinfo.Version}, &mcp.ServerOptions{
		Instructions: "A knowledge graph of entities, relations and observations for one project.",
	})
	memory.Register(srv, store)
	memory.RegisterResource(srv, store)

	p := &Project{ID: id, Store: store, Server: srv}
	r.projects[id] = p
	return p, nil
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

// Metadata is created atomically under the ID directory, including for read-only
// graph access. A mismatched or corrupt document is never silently replaced.
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
	data, err = json.Marshal(expected)
	if err != nil {
		return err
	}
	if err := graph.PublishFile(file, ".project-*.json", append(data, '\n'), 0); err != nil {
		return fmt.Errorf("publishing project metadata: %w", err)
	}
	return nil
}
