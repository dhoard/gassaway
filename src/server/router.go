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
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/dhoard/gassaway/src/buildinfo"
	"github.com/dhoard/gassaway/src/config"
	"github.com/dhoard/gassaway/src/projects"
)

// ProjectPath is the one path that addresses a project. Its {id} segment must be
// exactly 64 lowercase hexadecimal characters.
const ProjectPath = "/project/{id}"

// sessionHeader is the header the Streamable HTTP transport uses to resume a
// session.
const sessionHeader = "Mcp-Session-Id"

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
	s := &Server{cfg: cfg, registry: NewRegistry(cfg.ProjectsDir, cfg.MaxProjects, log), log: log}

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
//
// A Streamable HTTP client keeps a standalone SSE stream open for as long as it
// is connected, and that stream never goes idle, so the graceful wait cannot
// finish while one exists. Once the wait is exhausted the remaining connections
// are closed: the SDK exposes no way to close sessions (its closeAll is test
// only), and every acknowledged write is already durable — a tool call returns
// only after PublishFile has renamed and synced the graph — so a closed
// connection cannot lose data the client was told was stored. A drained-then-
// forced shutdown is a success; the reason is logged for the operator.
func (s *Server) Shutdown(ctx context.Context) error {
	err := s.httpServer.Shutdown(ctx)
	if err == nil {
		return nil
	}
	if closeErr := s.httpServer.Close(); closeErr != nil {
		return errors.Join(err, closeErr)
	}
	s.log.Warn("shutdown: closed connections that did not drain", "reason", err)
	return nil
}

// withProject resolves the path's project, stores it in the context, and only
// then delegates. Resolving here rather than in getServer means the failure modes
// (bad identity, foreign session, unreadable project metadata) become ordinary
// HTTP errors with a message, instead of the SDK's bare 400.
func (s *Server) withProject(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := resolveID(r)
		if err != nil {
			s.refuse(w, r, http.StatusBadRequest, err.Error())
			return
		}
		// A session is only usable at the project that minted it. The check is
		// made before the project is opened or created, so a foreign session
		// cannot reach another graph, cannot make this server build a runtime for
		// a project nobody asked for, and cannot leave a directory behind. 404 is
		// the status the protocol assigns to a session the server no longer has.
		if sessionID := r.Header.Get(sessionHeader); sessionID != "" && !sessionBelongsTo(sessionID, id) {
			s.refuse(w, r, http.StatusNotFound, "session not found")
			return
		}
		p, err := s.registry.Get(id)
		if err != nil {
			s.refuse(w, r, http.StatusInternalServerError, err.Error())
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), contextKey{}, p)))
	})
}

// sessionBelongsTo reports whether sessionID was minted by the server of project
// id. Session IDs are "<projectID>.<crypto/rand.Text()>", so the prefix is the
// project's; the suffix is what the SDK's session table matches, which is why a
// guessable prefix is not enough to use someone else's session.
func sessionBelongsTo(sessionID, id string) bool {
	rest, ok := strings.CutPrefix(sessionID, id+".")
	return ok && rest != ""
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

// refuse answers a request the server will not serve and records why. A client
// error is quoted back — the message is actionable and names no storage path —
// while a server error is logged in full and answered generically, so a path or
// an internal detail does not travel to the caller.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request, status int, reason string) {
	if status >= http.StatusInternalServerError {
		s.log.Error("request failed", "method", r.Method, "path", r.URL.Path, "status", status, "error", reason)
		writeError(w, status, "project storage is not available")
		return
	}
	s.log.Warn("request refused", "method", r.Method, "path", r.URL.Path, "status", status, "reason", reason)
	writeError(w, status, reason)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	count, err := s.registry.Count()
	if err != nil {
		s.refuse(w, r, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":   "ok",
		"version":  buildinfo.Version,
		"projects": count,
	})
}

func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	s.refuse(w, r, http.StatusNotFound, fmt.Sprintf("no route for %s %s: use /project/<id> or /healthz", r.Method, r.URL.Path))
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
