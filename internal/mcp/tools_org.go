package mcp

import (
	"context"
	"encoding/json"

	"github.com/noixdev/pulsix/internal/tenancy"
)

type noInput struct{}

func orgTools() []toolDef {
	return []toolDef{
		define("get_organization", "Organization", "The organization this connection acts in: its name and slug.",
			tenancy.AreaInventoryRead, readTool, func(ctx context.Context, c *caller, _ noInput) (any, error) {
				path, err := c.orgPath("")
				if err != nil {
					return nil, err
				}
				var out struct {
					Org json.RawMessage `json:"org"`
				}
				if _, err := c.get(ctx, path, nil, &out); err != nil {
					return nil, err
				}
				return out.Org, nil
			}),
	}
}
