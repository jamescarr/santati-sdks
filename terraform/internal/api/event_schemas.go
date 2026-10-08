package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
)

// SchemaVersion is one numbered JSON Schema of an event action.
type SchemaVersion struct {
	Version             int64           `json:"version"`
	Status              string          `json:"status"` // draft | published | deprecated
	Schema              json.RawMessage `json:"schema"`
	PublishedAt         *string         `json:"published_at"`
	DeprecatedAt        *string         `json:"deprecated_at"`
	DeprecationDeadline *string         `json:"deprecation_deadline"`
	CreatedAt           string          `json:"created_at"`
	UpdatedAt           string          `json:"updated_at"`
}

// schemaDocument is the body of a schema version create or replace.
type schemaDocument struct {
	Schema json.RawMessage `json:"schema"`
}

func schemaVersionsPath(action string) string {
	return "/api/v0/event-definitions/" + url.PathEscape(action) + "/schema-versions/"
}

func schemaVersionPath(action string, version int64) string {
	return schemaVersionsPath(action) + strconv.FormatInt(version, 10) + "/"
}

// CreateSchemaVersion adds a draft with the next version number. The action
// must already be defined; the control plane answers 404 otherwise.
func (c *Client) CreateSchemaVersion(ctx context.Context, action string, schema json.RawMessage) (*SchemaVersion, error) {
	var version SchemaVersion
	if err := c.do(ctx, http.MethodPost, schemaVersionsPath(action), nil, schemaDocument{Schema: schema}, &version); err != nil {
		return nil, err
	}
	return &version, nil
}

// GetSchemaVersion returns one schema version.
func (c *Client) GetSchemaVersion(ctx context.Context, action string, version int64) (*SchemaVersion, error) {
	var out SchemaVersion
	if err := c.do(ctx, http.MethodGet, schemaVersionPath(action, version), nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// UpdateSchemaVersion replaces a draft's document. It sends no If-Match, so the
// last write wins; the control plane answers 409 for a published version.
func (c *Client) UpdateSchemaVersion(ctx context.Context, action string, version int64, schema json.RawMessage) (*SchemaVersion, error) {
	var out SchemaVersion
	if err := c.do(ctx, http.MethodPut, schemaVersionPath(action, version), nil, schemaDocument{Schema: schema}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PublishSchemaVersion publishes a draft. It is idempotent, and it deprecates
// every older published version of the action.
func (c *Client) PublishSchemaVersion(ctx context.Context, action string, version int64) (*SchemaVersion, error) {
	var out SchemaVersion
	if err := c.do(ctx, http.MethodPost, schemaVersionPath(action, version)+"publish/", nil, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeleteSchemaVersion deletes a draft; the control plane answers 409 for a
// published version.
func (c *Client) DeleteSchemaVersion(ctx context.Context, action string, version int64) error {
	return c.do(ctx, http.MethodDelete, schemaVersionPath(action, version), nil, nil, nil)
}

// ListSchemaVersions returns every schema version of an action, newest first,
// following every page.
func (c *Client) ListSchemaVersions(ctx context.Context, action string) ([]SchemaVersion, error) {
	return listAll[SchemaVersion](ctx, c, schemaVersionsPath(action))
}
