// Copyright (c) 2026-present Douglas Hoard
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
// The error a client was given can quote all three — a schema validator prints
// the value it rejected, and "entity X not found" names X — so the record carries
// a redacted reason instead of the error string (see logReason).
package server

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
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
				log.Warn("tool call failed", "project", projectID, "tool", tool, "error", toolErrorReason(res), "duration_ms", elapsed)
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

// toolErrorReason is why a tool call failed, in a form that is safe to log. It is
// empty only if a handler built an error result without an error value.
func toolErrorReason(res mcp.Result) string {
	result, ok := res.(*mcp.CallToolResult)
	if !ok {
		return ""
	}
	if err := result.GetError(); err != nil {
		return logReason(err)
	}
	return ""
}

// argumentErrorPrefix opens every error the SDK reports for arguments its schema
// validator rejected.
const argumentErrorPrefix = `validating "arguments": `

// logReason turns a tool error into the reason an operator's log records.
//
// Two kinds of error quote what the client sent. An error that knows it does
// implements Redacted and is logged as that. A schema-validation error is
// rebuilt from the parts that come from the schema alone. Anything else — a
// storage failure, a limit — is this server's own wording about its own state
// and is logged as it is.
func logReason(err error) string {
	var redacted interface{ Redacted() string }
	if errors.As(err, &redacted) {
		return redacted.Redacted()
	}
	if detail, ok := strings.CutPrefix(err.Error(), argumentErrorPrefix); ok {
		return redactValidation(detail)
	}
	return err.Error()
}

var (
	// schemaStep is one "validating <schema location>" step of a validation
	// error. The location is a path through the published schema, so it holds
	// schema keywords and declared property names and nothing a client sent.
	schemaStep = regexp.MustCompile(`^validating (root|/\S*)$`)
	// schemaKeyword is the JSON Schema keyword that rejected the value.
	schemaKeyword = regexp.MustCompile(`^[A-Za-z]+$`)
)

// redactValidation reduces a schema-validation error to where the arguments
// failed and which rule they broke. The validator's message has the form
//
//	validating root: validating /properties/entities: type: <value> has type "string", want "array"
//
// and the rejected value is printed in full, which can be a whole batch of
// entities. Only three parts are kept: the schema location, the keyword, and a
// tail known not to carry the value — the missing property names of "required",
// which are names the schema declares, and the two type names of "type".
func redactValidation(detail string) string {
	const fallback = "invalid arguments"

	parts := strings.Split(detail, ": ")
	location := ""
	i := 0
	for ; i < len(parts); i++ {
		m := schemaStep.FindStringSubmatch(parts[i])
		if m == nil {
			break
		}
		location = m[1]
	}
	if location == "" || i == len(parts) || !schemaKeyword.MatchString(parts[i]) {
		return fallback
	}
	keyword := parts[i]
	reason := fallback + " at " + location + ": " + keyword

	rest := strings.Join(parts[i+1:], ": ")
	switch keyword {
	case "required":
		// "missing properties: [...]" lists required names from the schema.
		reason += ": " + rest
	case "type":
		// The value comes first and may itself contain the separator, so the
		// type names are taken from the last occurrence: what follows it is two
		// quoted type names and cannot hold another one.
		const marker = " has type "
		if at := strings.LastIndex(rest, marker); at >= 0 {
			reason += ": has type " + rest[at+len(marker):]
		}
	}
	return reason
}
