package api

import "context"

// Organization is one of the team's organizations.
type Organization struct {
	ID              int64  `json:"id"`
	ExternalID      string `json:"external_id"`
	Name            string `json:"name"`
	RetentionMonths int64  `json:"retention_months"`
	Region          string `json:"region"`
	LegalHold       bool   `json:"legal_hold"`
	CreatedAt       string `json:"created_at"`
}

// ListOrganizations returns every organization, following every page.
func (c *Client) ListOrganizations(ctx context.Context) ([]Organization, error) {
	return listAll[Organization](ctx, c, "/api/v0/organizations/")
}
