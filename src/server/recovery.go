// Copyright (c) 2026-present Douglas Hoard
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file keeps one request from taking the whole process down.
//
// The SDK invokes handlers on goroutines of its own, so a panic inside one is not
// caught by net/http's per-connection recovery: it propagates out of the process.
// That is a single-tenant annoyance in a one-project server and an outage here,
// where one process serves every project. A receiving middleware therefore runs
// every method — tools, resources, prompts, and the SDK's own argument paths —
// under recover, and turns a panic into an ordinary JSON-RPC internal error.
//
// Nothing about the panic reaches the caller except the wording "internal error":
// a recovered value can quote graph content, and the client is not the place to
// disclose it. The value is logged instead.
package server

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// recoverPanics returns middleware that converts a panic in a handler into a
// JSON-RPC internal error. projectID scopes the log record; it is the project
// whose server installed the middleware.
func recoverPanics(projectID string, log *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (res mcp.Result, err error) {
			defer func() {
				recovered := recover()
				if recovered == nil {
					return
				}
				log.Error("panic in handler", "project", projectID, "method", method, "panic", fmt.Sprint(recovered))
				res, err = nil, &jsonrpc.Error{
					Code:    jsonrpc.CodeInternalError,
					Message: "internal error",
				}
			}()
			return next(ctx, method, req)
		}
	}
}
