package santati_test

import (
	"context"
	"encoding/json"
	"iter"

	santati "github.com/jamescarr/santati-sdks/go"
)

// schemaInput is the part of a case's input the schema operations read.
type schemaInput struct {
	Action  string `json:"action"`
	Version int    `json:"version"`
	Params  struct {
		Limit  int    `json:"limit"`
		Cursor string `json:"cursor"`
	} `json:"params"`
	Definition wireDefinition `json:"definition"`
	Changes    wireDefinition `json:"changes"`
	Schema     map[string]any `json:"schema"`
	IfMatch    string         `json:"if_match"`
	Packs      []string       `json:"packs"`
}

// wireDefinition is a vector's definition or changes: nil members are absent.
type wireDefinition struct {
	Action             string   `json:"action"`
	NewAction          *string  `json:"new_action"`
	Description        *string  `json:"description"`
	AllowedTargetTypes []string `json:"allowed_target_types"`
	IsActive           *bool    `json:"is_active"`
}

// runSchemaOperation runs one of the schema operations and returns it in the
// vector's wire shape. handled is false for an operation it does not know.
func runSchemaOperation(ctx context.Context, client *santati.Client, operation string, raw json.RawMessage) (actual any, handled bool, err error) {
	var input schemaInput
	if err := json.Unmarshal(raw, &input); err != nil {
		return nil, true, err
	}
	schemas := client.Schemas
	pageParams := santati.PageParams{Limit: input.Params.Limit, Cursor: input.Params.Cursor}
	switch operation {
	case "list_definitions":
		page, err := schemas.ListDefinitions(ctx, pageParams)
		if err != nil {
			return nil, true, err
		}
		return cursorPageValue(page.Results, page.NextCursor), true, nil
	case "iterate_definitions":
		items, err := collectSeq(schemas.IterateDefinitions(ctx, pageParams))
		return items, true, err
	case "get_definition":
		definition, err := schemas.GetDefinition(ctx, input.Action)
		return definition, true, err
	case "create_definition":
		definition, err := schemas.CreateDefinition(ctx, santati.DefinitionInput{
			Action:             input.Definition.Action,
			Description:        input.Definition.Description,
			AllowedTargetTypes: input.Definition.AllowedTargetTypes,
			IsActive:           input.Definition.IsActive,
		})
		return definition, true, err
	case "update_definition":
		definition, err := schemas.UpdateDefinition(ctx, input.Action, santati.DefinitionUpdate{
			NewAction:          input.Changes.NewAction,
			Description:        input.Changes.Description,
			AllowedTargetTypes: input.Changes.AllowedTargetTypes,
			IsActive:           input.Changes.IsActive,
		})
		return definition, true, err
	case "delete_definition":
		return nil, true, schemas.DeleteDefinition(ctx, input.Action)
	case "list_versions":
		page, err := schemas.ListVersions(ctx, input.Action, pageParams)
		if err != nil {
			return nil, true, err
		}
		return cursorPageValue(page.Results, page.NextCursor), true, nil
	case "iterate_versions":
		items, err := collectSeq(schemas.IterateVersions(ctx, input.Action, pageParams))
		return items, true, err
	case "get_version":
		return versionValue(schemas.GetVersion(ctx, input.Action, input.Version))
	case "create_version":
		return versionValue(schemas.CreateVersion(ctx, input.Action, input.Schema))
	case "update_version":
		return versionValue(schemas.UpdateVersion(ctx, input.Action, input.Version, input.Schema, input.IfMatch))
	case "delete_version":
		return nil, true, schemas.DeleteVersion(ctx, input.Action, input.Version)
	case "publish_version":
		return versionValue(schemas.PublishVersion(ctx, input.Action, input.Version))
	case "deprecate_version":
		return versionValue(schemas.DeprecateVersion(ctx, input.Action, input.Version))
	case "check_schema":
		check, err := schemas.CheckSchema(ctx, input.Action, input.Schema)
		return check, true, err
	case "list_standard_packs":
		catalog, err := schemas.ListStandardPacks(ctx)
		return catalog, true, err
	case "install_standard_packs":
		result, err := schemas.InstallStandardPacks(ctx, input.Packs)
		return result, true, err
	default:
		return nil, false, nil
	}
}

// collectSeq drains an iterator, returning what it yielded before an error.
func collectSeq[T any](seq iter.Seq2[*T, error]) ([]*T, error) {
	items := []*T{}
	for item, err := range seq {
		if err != nil {
			return items, err
		}
		items = append(items, item)
	}
	return items, nil
}

// cursorPageValue is a page in the vector's wire shape.
func cursorPageValue(results any, next *string) any {
	value := map[string]any{"results": results, "next_cursor": nil}
	if next != nil {
		value["next_cursor"] = *next
	}
	return value
}

// versionValue is a SchemaVersionResult in the vector's wire shape.
func versionValue(result *santati.SchemaVersionResult, err error) (any, bool, error) {
	if err != nil {
		return nil, true, err
	}
	return map[string]any{"schema_version": result.SchemaVersion, "etag": nullIfEmpty(result.ETag)}, true, nil
}
