package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/jsonschema-go/jsonschema"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/noixdev/pulsix/internal/tenancy"
)

// defineExact is define for a tool whose input carries free-form values (a
// run request's extra vars): the SDK's typed path decodes the arguments
// into float64 numbers, which rounds an integer past 2^53, so this one
// takes the raw arguments, validates them against In's schema exactly as
// the typed path does (tune adjusts that schema where struct tags cannot,
// such as numeric bounds) and decodes them with json.Number. The handler
// and its result are define's.
func defineExact[In any](name, title, description string, area tenancy.Area, k kind, tune func(*jsonschema.Schema), h func(ctx context.Context, c *caller, in In) (any, error)) toolDef {
	schema, err := jsonschema.For[In](nil)
	if err != nil {
		panic(fmt.Sprintf("tool %s: input schema: %v", name, err))
	}
	if tune != nil {
		tune(schema)
	}
	resolved, err := schema.Resolve(&jsonschema.ResolveOptions{ValidateDefaults: true})
	if err != nil {
		panic(fmt.Sprintf("tool %s: resolve input schema: %v", name, err))
	}
	return toolDef{name: name, area: area, register: func(s *sdk.Server, c *caller) {
		destructive := false
		tool := &sdk.Tool{
			Name: name, Title: title, Description: description, InputSchema: schema,
			Annotations: &sdk.ToolAnnotations{Title: title, ReadOnlyHint: k == readTool, DestructiveHint: &destructive},
		}
		s.AddTool(tool, func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
			in, err := decodeExact[In](req.Params.Arguments, resolved)
			if err != nil {
				return toolError(err), nil
			}
			out, err := h(ctx, c, in)
			if err != nil {
				return toolError(err), nil
			}
			res, _, err := textResult(out)
			return res, err
		})
	}}
}

// decodeExact validates raw against resolved and decodes it into In with
// json.Number for every number held in an untyped field.
func decodeExact[In any](raw json.RawMessage, resolved *jsonschema.Resolved) (In, error) {
	var in In
	check := map[string]any{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &check); err != nil {
			return in, fmt.Errorf("unmarshaling arguments: %w", err)
		}
	}
	var instance any = check
	if err := resolved.Validate(&instance); err != nil {
		return in, fmt.Errorf("validating \"arguments\": %w", err)
	}
	if len(raw) == 0 {
		return in, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&in); err != nil {
		return in, fmt.Errorf("unmarshaling arguments: %w", err)
	}
	return in, nil
}

func toolError(err error) *sdk.CallToolResult {
	var res sdk.CallToolResult
	res.SetError(err)
	return &res
}
