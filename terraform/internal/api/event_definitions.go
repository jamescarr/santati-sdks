package api

import "context"

// EventDefinition is one action in the team's event catalog.
type EventDefinition struct {
	Action             string   `json:"action"`
	Description        string   `json:"description"`
	AllowedTargetTypes []string `json:"allowed_target_types"`
	IsActive           bool     `json:"is_active"`
	CreatedAt          string   `json:"created_at"`
	UpdatedAt          string   `json:"updated_at"`
}

// ListEventDefinitions returns every event definition, following every page.
func (c *Client) ListEventDefinitions(ctx context.Context) ([]EventDefinition, error) {
	return listAll[EventDefinition](ctx, c, "/api/v0/event-definitions/")
}
