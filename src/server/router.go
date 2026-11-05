// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file is the HTTP edge: routing, project resolution, the health endpoint,
// and the wiring of the SDK's Streamable HTTP handler.
//
// The surface is two URLs. /project/<id> addresses one project's memory over
// Streamable HTTP, and /healthz reports the service, its version and how many
// projects it stores. The project identity is the full SHA-256 ID in the path: an
// identity anywhere else — a header, a query, a second segment — is not read, and
// no request falls back to a default project.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/projects"
)

// ProjectPath is the one path that addresses a project. Its {id} segment must be
// exactly 64 lowercase hexadecimal characters.
const ProjectPath = "/project/{id}"

type contextKey struct{}

// Server is the assembled HTTP service.
type Server struct {
	cfg        config.Config
	registry   *Registry
	handler    http.Handler
	httpServer *http.Server
	log        *slog.Logger
}

// New assembles the service: registry, MCP handler, routes.
func New(cfg config.Config, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	s := &Server{cfg: cfg, registry: NewRegistry(cfg.ProjectsDir, cfg.MaxProjects), log: log}

	streamable := mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		p, ok := projectFrom(r.Context())
		if !ok {
			// The middleware always sets this; nil is the SDK's own 400.
			return nil
		}
		return p.Server
	}, &mcp.StreamableHTTPOptions{
		MaxRequestBodyBytes: cfg.MaxBodyBytes,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealth)
	mux.Handle(ProjectPath, s.withProject(streamable))
	mux.HandleFunc("/", s.handleNotFound)

	s.handler = mux
	s.httpServer = &http.Server{
		Handler: mux,
		// A slow or stalled client must not hold a header read open forever.
		ReadHeaderTimeout: 10 * time.Second,
		// WriteTimeout is deliberately unset: a Streamable HTTP response may be a
		// long-lived SSE stream that a write deadline would cut. IdleTimeout bounds
		// the keep-alive connections that are not carrying a stream instead.
		IdleTimeout: 120 * time.Second,
	}
	return s
}

// Handler exposes the router for tests.
func (s *Server) Handler() http.Handler { return s.handler }

// Serve accepts connections on ln until the server is shut down.
func (s *Server) Serve(ln net.Listener) error {
	err := s.httpServer.Serve(ln)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown stops the listener and waits for in-flight requests, bounded by ctx.
func (s *Server) Shutdown(ctx context.Context) error {
	return s.httpServer.Shutdown(ctx)
}

// withProject resolves the path's project, stores it in the context, and only
// then delegates. Resolving here rather than in getServer means the failure modes
// (bad name, cap, unwritable directory) become ordinary HTTP errors with a
// message, instead of the SDK's bare 400.
func (s *Server) withProject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := resolveID(r)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		p, err := s.registry.Get(id)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, ErrTooManyProjects) {
				status = http.StatusTooManyRequests
			}
			writeError(w, status, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, p)))
	})
}

func projectFrom(ctx context.Context) (*Project, bool) {
	p, ok := ctx.Value(contextKey{}).(*Project)
	return p, ok
}

// resolveID validates the single identity the surface accepts. The ID is used
// as-is; nothing is trimmed, folded or re-hashed.
func resolveID(r *http.Request) (string, error) {
	id := r.PathValue("id")
	if err := projects.Validate(id); err != nil {
		return "", err
	}
	return id, nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	count, err := s.registry.Count()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  buildinfo.Version,
		"projects": count,
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotFound, fmt.Sprintf("no route for %s %s: use /project/<id> or /healthz", r.Method, r.URL.Path))
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeError answers with a JSON error object. HTML escaping is off so a message
// that names a URL form (/project/<id>) reads as written in curl output.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(map[string]string{"error": message})
}
