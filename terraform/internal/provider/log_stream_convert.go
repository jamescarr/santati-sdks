package provider

import (
	"context"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"

	"github.com/jamescarr/terraform-provider-santati/internal/api"
)

type streamConfigModel struct {
	URL            types.String `tfsdk:"url"`
	ContentType    types.String `tfsdk:"content_type"`
	Headers        types.Map    `tfsdk:"headers"`
	TimeoutSeconds types.Int64  `tfsdk:"timeout_seconds"`
}

type streamAuthModel struct {
	HeaderName  types.String `tfsdk:"header_name"`
	HeaderValue types.String `tfsdk:"header_value"`
}

type matchRulesModel struct {
	Actions         types.List `tfsdk:"actions"`
	ActorTypes      types.List `tfsdk:"actor_types"`
	OrganizationIDs types.List `tfsdk:"organization_ids"`
	Metadata        types.List `tfsdk:"metadata"`
}

type matchClauseModel struct {
	Key   types.String `tfsdk:"key"`
	Op    types.String `tfsdk:"op"`
	Value types.String `tfsdk:"value"`
}

var (
	stringListType = types.ListType{ElemType: types.StringType}

	streamConfigAttrTypes = map[string]attr.Type{
		"url":             types.StringType,
		"content_type":    types.StringType,
		"headers":         types.MapType{ElemType: types.StringType},
		"timeout_seconds": types.Int64Type,
	}
	matchClauseAttrTypes = map[string]attr.Type{
		"key":   types.StringType,
		"op":    types.StringType,
		"value": types.StringType,
	}
	matchRulesAttrTypes = map[string]attr.Type{
		"actions":          stringListType,
		"actor_types":      stringListType,
		"organization_ids": stringListType,
		"metadata":         types.ListType{ElemType: types.ObjectType{AttrTypes: matchClauseAttrTypes}},
	}
)

// expandStream builds the request body from a plan. It always carries the
// whole of `config` (with explicit content type and timeout) and of
// `match_rules` (`{}` when the block is absent): the server replaces both
// wholesale, so what is sent is what is stored. Auth is the caller's call.
func expandStream(ctx context.Context, m logStreamModel) (api.StreamWrite, diag.Diagnostics) {
	var diags diag.Diagnostics
	write := api.StreamWrite{
		Name:            m.Name.ValueString(),
		Trail:           m.Trail.ValueString(),
		DestinationType: m.DestinationType.ValueString(),
		Status:          m.Status.ValueString(),
	}

	var config streamConfigModel
	diags.Append(m.Config.As(ctx, &config, basetypes.ObjectAsOptions{})...)
	if diags.HasError() {
		return write, diags
	}
	write.Config = api.HTTPSConfig{
		URL:            config.URL.ValueString(),
		ContentType:    config.ContentType.ValueString(),
		TimeoutSeconds: config.TimeoutSeconds.ValueInt64(),
	}
	if !config.Headers.IsNull() {
		diags.Append(config.Headers.ElementsAs(ctx, &write.Config.Headers, false)...)
	}

	if !m.MatchRules.IsNull() {
		var rules matchRulesModel
		diags.Append(m.MatchRules.As(ctx, &rules, basetypes.ObjectAsOptions{})...)
		if diags.HasError() {
			return write, diags
		}
		var d diag.Diagnostics
		write.MatchRules, d = expandMatchRules(ctx, rules)
		diags.Append(d...)
	}
	return write, diags
}

func expandMatchRules(ctx context.Context, m matchRulesModel) (api.MatchRules, diag.Diagnostics) {
	var diags diag.Diagnostics
	var rules api.MatchRules
	for _, list := range []struct {
		from types.List
		to   *[]string
	}{
		{m.Actions, &rules.Actions},
		{m.ActorTypes, &rules.ActorTypes},
		{m.OrganizationIDs, &rules.OrganizationIDs},
	} {
		if !list.from.IsNull() {
			diags.Append(list.from.ElementsAs(ctx, list.to, false)...)
		}
	}

	if !m.Metadata.IsNull() {
		var clauses []matchClauseModel
		diags.Append(m.Metadata.ElementsAs(ctx, &clauses, false)...)
		for _, clause := range clauses {
			rules.Metadata = append(rules.Metadata, api.MatchClause{
				Key:   clause.Key.ValueString(),
				Op:    clause.Op.ValueString(),
				Value: clause.Value.ValueString(),
			})
		}
	}
	return rules, diags
}

func expandAuth(ctx context.Context, auth types.Object) (*api.HTTPSAuth, diag.Diagnostics) {
	var model streamAuthModel
	diags := auth.As(ctx, &model, basetypes.ObjectAsOptions{})
	if diags.HasError() {
		return nil, diags
	}
	return &api.HTTPSAuth{
		HeaderName:  model.HeaderName.ValueString(),
		HeaderValue: model.HeaderValue.ValueString(),
	}, diags
}

// flattenStream copies a server response into m. It leaves m.Auth alone: the
// server never returns the credential, so create and update keep the plan's
// and read keeps the prior state's.
func flattenStream(ctx context.Context, s *api.Stream, m *logStreamModel) diag.Diagnostics {
	var diags diag.Diagnostics

	m.ID = types.StringValue(strconv.FormatInt(s.ID, 10))
	m.Name = types.StringValue(s.Name)
	m.Trail = types.StringValue(s.Trail)
	m.Status = types.StringValue(s.Status)
	m.DestinationType = types.StringValue(s.Destination.DestinationType)
	m.ConsecutiveFailures = types.Int64Value(s.ConsecutiveFailures)
	m.LastError = types.StringValue(s.LastError)
	m.LastErrorAt = nullableString(s.LastErrorAt)
	m.ResumeCursor = nullableString(s.ResumeCursor)
	m.CreatedAt = types.StringValue(s.CreatedAt)
	m.UpdatedAt = types.StringValue(s.UpdatedAt)

	var d diag.Diagnostics
	m.Config, d = flattenConfig(ctx, s.Destination.Config)
	diags.Append(d...)
	m.MatchRules, d = flattenMatchRules(ctx, s.MatchRules)
	diags.Append(d...)
	return diags
}

func flattenConfig(ctx context.Context, c api.HTTPSConfig) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	headers := types.MapNull(types.StringType)
	if len(c.Headers) > 0 {
		var d diag.Diagnostics
		headers, d = types.MapValueFrom(ctx, types.StringType, c.Headers)
		diags.Append(d...)
	}
	obj, d := types.ObjectValue(streamConfigAttrTypes, map[string]attr.Value{
		"url":             types.StringValue(c.URL),
		"content_type":    types.StringValue(c.ContentType),
		"headers":         headers,
		"timeout_seconds": types.Int64Value(c.TimeoutSeconds),
	})
	diags.Append(d...)
	return obj, diags
}

// flattenMatchRules maps every empty list to null, and a rule set with nothing
// in it to a null block, so that omitting `match_rules` round-trips.
func flattenMatchRules(ctx context.Context, r api.MatchRules) (types.Object, diag.Diagnostics) {
	var diags diag.Diagnostics
	null := types.ObjectNull(matchRulesAttrTypes)

	actions, d := stringListOrNull(ctx, r.Actions)
	diags.Append(d...)
	actorTypes, d := stringListOrNull(ctx, r.ActorTypes)
	diags.Append(d...)
	organizationIDs, d := stringListOrNull(ctx, r.OrganizationIDs)
	diags.Append(d...)

	metadata := types.ListNull(types.ObjectType{AttrTypes: matchClauseAttrTypes})
	if len(r.Metadata) > 0 {
		clauses := make([]matchClauseModel, len(r.Metadata))
		for i, clause := range r.Metadata {
			clauses[i] = matchClauseModel{
				Key:   types.StringValue(clause.Key),
				Op:    types.StringValue(clause.Op),
				Value: types.StringValue(clause.Value),
			}
		}
		metadata, d = types.ListValueFrom(ctx, types.ObjectType{AttrTypes: matchClauseAttrTypes}, clauses)
		diags.Append(d...)
	}
	if diags.HasError() {
		return null, diags
	}

	if actions.IsNull() && actorTypes.IsNull() && organizationIDs.IsNull() && metadata.IsNull() {
		return null, diags
	}
	obj, d := types.ObjectValue(matchRulesAttrTypes, map[string]attr.Value{
		"actions":          actions,
		"actor_types":      actorTypes,
		"organization_ids": organizationIDs,
		"metadata":         metadata,
	})
	diags.Append(d...)
	return obj, diags
}

func stringListOrNull(ctx context.Context, values []string) (types.List, diag.Diagnostics) {
	if len(values) == 0 {
		return types.ListNull(types.StringType), nil
	}
	return types.ListValueFrom(ctx, types.StringType, values)
}

func nullableString(s *string) types.String {
	if s == nil {
		return types.StringNull()
	}
	return types.StringValue(*s)
}
