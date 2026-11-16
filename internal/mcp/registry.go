package mcp

import (
	"context"
	"encoding/json"
	"fmt"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// toolDef is one tool: the area that unlocks it and how it registers on a
// per-request server bound to the caller.
type toolDef struct {
	name     string
	area     tenancy.Area
	register func(s *sdk.Server, c *caller)
}

// allowed reports whether one of areas unlocks the tool. Write areas
// include their read area, so the check is on the permissions: a tool is
// listed when any of the token's areas grants every permission the tool's
// own area grants.
func (t toolDef) allowed(areas []tenancy.Area) bool {
	need, ok := tenancy.AreaPermissions(t.area)
	if !ok {
		return false
	}
	for _, p := range need {
		if !tenancy.AreasAllow(areas, p) {
			return false
		}
	}
	return true
}

// kind tells read tools from write tools for the annotations.
type kind int

const (
	readTool kind = iota
	writeTool
)

// define builds a tool whose handler returns a value rendered as indented
// JSON text, or an error rendered as a tool error.
func define[In any](name, title, description string, area tenancy.Area, k kind, h func(ctx context.Context, c *caller, in In) (any, error)) toolDef {
	return toolDef{name: name, area: area, register: func(s *sdk.Server, c *caller) {
		destructive := false
		tool := &sdk.Tool{
			Name: name, Title: title, Description: description,
			Annotations: &sdk.ToolAnnotations{Title: title, ReadOnlyHint: k == readTool, DestructiveHint: &destructive},
		}
		sdk.AddTool(s, tool, func(ctx context.Context, _ *sdk.CallToolRequest, in In) (*sdk.CallToolResult, any, error) {
			out, err := h(ctx, c, in)
			if err != nil {
				return nil, nil, err
			}
			return textResult(out)
		})
	}}
}

func textResult(v any) (*sdk.CallToolResult, any, error) {
	var text string
	switch t := v.(type) {
	case string:
		text = t
	case json.RawMessage:
		text = string(t)
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return nil, nil, fmt.Errorf("encode result: %w", err)
		}
		text = string(b)
	}
	return &sdk.CallToolResult{Content: []sdk.Content{&sdk.TextContent{Text: text}}}, nil, nil
}

// withoutSchemaLink drops the top-level "$schema" of a REST response
// body. The REST layer derives it from the request's host, which for the
// in-process dispatch is not the address Pulsix is reached at, so it would
// only point the assistant somewhere wrong. A body that is not an object
// with that key is returned as it is.
func withoutSchemaLink(raw json.RawMessage) json.RawMessage {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return raw
	}
	if _, ok := obj["$schema"]; !ok {
		return raw
	}
	delete(obj, "$schema")
	b, err := json.Marshal(obj)
	if err != nil {
		return raw
	}
	return b
}

// registry lists every tool. Its order is the order tools/list shows.
func registry() []toolDef {
	var out []toolDef
	out = append(out, orgTools()...)
	out = append(out, readTools()...)
	out = append(out, writeTools()...)
	out = append(out, jobTools()...)
	return out
}
