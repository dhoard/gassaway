// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// Tests for crash containment: a panicking handler is an error, not an outage.
//
// These are internal tests because they inject a panicking tool and resource
// into a project server the registry built, which is the only way to prove the
// middleware is installed on the server a client actually talks to.
package server

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const panicProjectID = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

func connectInMemory(t *testing.T, srv *mcp.Server) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	go func() { _, _ = srv.Connect(ctx, serverTransport, nil) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "recovery-test", Version: "0"}, nil).
		Connect(ctx, clientTransport, nil)
	if err != nil {
		t.Fatalf("connecting: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestPanicInToolHandlerIsContained(t *testing.T) {
	var logs strings.Builder
	registry := NewRegistry(t.TempDir(), 10, slog.New(slog.NewTextHandler(&logs, nil)))
	project, err := registry.Get(panicProjectID)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}

	mcp.AddTool(project.Server, &mcp.Tool{Name: "boom", Description: "panics on purpose"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			var m map[string]string
			m["x"] = "y" // panics: assignment to entry in nil map
			return nil, struct{}{}, nil
		})
	mcp.AddTool(project.Server, &mcp.Tool{Name: "fine", Description: "answers"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, map[string]string, error) {
			return nil, map[string]string{"ok": "yes"}, nil
		})

	cs := connectInMemory(t, project.Server)

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "boom"}); err == nil {
		t.Fatal("a panicking tool must return an error, not a result")
	}

	// The process is still serving: the same session, the same server.
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "fine"})
	if err != nil {
		t.Fatalf("the server stopped serving after a panic: %v", err)
	}
	if res.IsError {
		t.Fatalf("the call after a panic failed: %v", res.GetError())
	}

	if !strings.Contains(logs.String(), "panic in handler") {
		t.Fatalf("the panic was not logged; log was:\n%s", logs.String())
	}
	if !strings.Contains(logs.String(), panicProjectID) {
		t.Fatalf("the panic record does not name the project; log was:\n%s", logs.String())
	}
}

func TestPanicInResourceHandlerIsContained(t *testing.T) {
	registry := NewRegistry(t.TempDir(), 10, nil)
	project, err := registry.Get(panicProjectID)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	project.Server.AddResource(&mcp.Resource{URI: "memory://boom", Name: "boom"},
		func(context.Context, *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
			panic(errors.New("resource panic"))
		})

	cs := connectInMemory(t, project.Server)
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "memory://boom"}); err == nil {
		t.Fatal("a panicking resource must return an error")
	}

	// The real resource still works.
	if _, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "memory://knowledge-graph"}); err != nil {
		t.Fatalf("the server stopped serving resources after a panic: %v", err)
	}
}

// The recovered value must not travel to the client: it can quote graph content.
func TestRecoveredPanicIsNotDisclosed(t *testing.T) {
	registry := NewRegistry(t.TempDir(), 10, nil)
	project, err := registry.Get(panicProjectID)
	if err != nil {
		t.Fatalf("opening project: %v", err)
	}
	mcp.AddTool(project.Server, &mcp.Tool{Name: "leaky", Description: "panics with content"},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, struct{}, error) {
			panic("secret-observation-text")
		})

	cs := connectInMemory(t, project.Server)
	_, err = cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "leaky"})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "secret-observation-text") {
		t.Fatalf("the panic value was disclosed to the client: %v", err)
	}
}
