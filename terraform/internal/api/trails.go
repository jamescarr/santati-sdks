package api

import (
	"context"
	"net/http"
	"net/url"
)

// Trail is an audit trail: where producers send events for one region.
type Trail struct {
	Name      string `json:"name"`
	Region    string `json:"region"`
	IngestURL string `json:"ingest_url"`
}

// TrailCreate is the body of a trail creation.
type TrailCreate struct {
	Name string `json:"name"`
	// Region is omitted when empty, which means the deployment's default.
	Region string `json:"region,omitempty"`
}

const trailsPath = "/api/v0/trails/"

// ListTrails returns every trail the key may write to, across regions. The
// endpoint is not paginated.
func (c *Client) ListTrails(ctx context.Context) ([]Trail, error) {
	var trails []Trail
	if err := c.do(ctx, http.MethodGet, trailsPath, nil, nil, &trails); err != nil {
		return nil, err
	}
	return trails, nil
}

// CreateTrail creates a trail.
func (c *Client) CreateTrail(ctx context.Context, in TrailCreate) (*Trail, error) {
	var trail Trail
	if err := c.do(ctx, http.MethodPost, trailsPath, nil, in, &trail); err != nil {
		return nil, err
	}
	return &trail, nil
}

// DeleteTrail deletes a trail; an empty region means the deployment's default.
func (c *Client) DeleteTrail(ctx context.Context, name, region string) error {
	var query url.Values
	if region != "" {
		query = url.Values{"region": {region}}
	}
	return c.do(ctx, http.MethodDelete, trailsPath+url.PathEscape(name)+"/", query, nil, nil)
}
