// Copyright (c) 2026-present Douglas Hoard. All rights reserved.
// Use of this source code is governed by the MIT license,
// which can be found in the LICENSE file.

// This file makes a failed MCP call leave evidence where an operator looks.
//
// The SDK routes every incoming method through its receiving middleware, which is
// the only place a rejection is visible: an argument-validation failure never
// reaches a tool handler, so handler-level instrumentation cannot see the failure
// that matters. The middleware below times each tools/call, classifies the
// outcome, and emits exactly one record.
//
// What is deliberately absent: tool arguments, entity names and observation text.
// The error string is logged because it is the diagnostic the client already
// received; it stays on the host, in the same trust domain as the graph files.
package server

import (
	"context"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// methodCallTool is the only method this middleware classifies in detail; every
// other method costs one comparison.
const methodCallTool = "tools/call"

// toolCallLogger returns middleware that records the outcome of each tools/call
// for one project: Warn for a tool error, Error for a transport or protocol
// error, Debug for a success. projectID scopes the record.
func toolCallLogger(projectID string, log *slog.Logger) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != methodCallTool {
				return next(ctx, method, req)
			}
			tool := calledTool(req)
			start := time.Now()

			res, err := next(ctx, method, req)

			elapsed := time.Since(start).Milliseconds()
			switch {
			case err != nil:
				log.Error("tool call failed", "project", projectID, "tool", tool, "error", err.Error(), "duration_ms", elapsed)
			case isToolError(res):
				log.Warn("tool call failed", "project", projectID, "tool", tool, "error", toolErrorText(res), "duration_ms", elapsed)
			default:
				log.Debug("tool call", "project", projectID, "tool", tool, "duration_ms", elapsed)
			}
			return res, err
		}
	}
}

// calledTool is the tool name, or "" when the request shape is not one this
// server knows; the log record then still carries the project and the method.
// The receive side hands the middleware the raw parameters, whose arguments have
// not been decoded yet; the decoded form is accepted too so the helper does not
// depend on which side of the SDK it is called from.
func calledTool(req mcp.Request) string {
	if req == nil {
		return ""
	}
	switch params := req.GetParams().(type) {
	case *mcp.CallToolParams:
		return params.Name
	case *mcp.CallToolParamsRaw:
		return params.Name
	}
	return ""
}

func isToolError(res mcp.Result) bool {
	result, ok := res.(*mcp.CallToolResult)
	return ok && result.IsError
}

// toolErrorText is the error the model was shown. It is empty only if a handler
// built an error result without an error value.
func toolErrorText(res mcp.Result) string {
	result, ok := res.(*mcp.CallToolResult)
	if !ok {
		return ""
	}
	if err := result.GetError(); err != nil {
		return err.Error()
	}
	return ""
}
