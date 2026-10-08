package santati

import (
	"context"
	"iter"
	"net/http"

	"github.com/jamescarr/santati-sdks/go/internal/core"
)

// Schemas manages event definitions, their schema versions and the standard
// packs. It shares the client's key, base URL, headers and retry policy.
//
// Every operation but CreateVersion retries like Events; CreateVersion is sent
// once, because a repeat would create a second draft. An empty action fails
// with a KindValidation *Error (field "action") before any request;
// everything else is forwarded as given and the server decides.
type Schemas struct {
	client *Client
}

func (s *Schemas) api() *core.EventDefinitionsAPIService {
	return s.client.api.EventDefinitionsAPI
}

func requireAction(action string) error {
	if action == "" {
		return validationError("action", "action is required")
	}
	return nil
}

// check classifies one generated call that should have answered the expected
// status: a transport failure, a non-2xx, or an unexpected or undecodable 2xx
// is an *Error; nil means success.
func (c *Client) check(resp *http.Response, err error, expected int) error {
	switch {
	case resp == nil:
		return transportError(err)
	case resp.StatusCode >= 300:
		return c.httpError(resp)
	case resp.StatusCode != expected || err != nil:
		return apiError(resp.StatusCode, "")
	default:
		return nil
	}
}

// attempt runs one generated call that answers a value, requiring the expected
// status.
func attempt[T any](c *Client, expected int, execute func() (T, *http.Response, error)) (T, *http.Response, error) {
	value, resp, err := execute()
	if failure := c.check(resp, err, expected); failure != nil {
		var zero T
		return zero, nil, failure
	}
	return value, resp, nil
}

// versionAttempt is attempt for the operations that answer a schema version:
// the result carries the response's ETag.
func versionAttempt(c *Client, expected int, execute func() (*core.EventSchemaVersion, *http.Response, error)) (*SchemaVersionResult, error) {
	version, resp, err := attempt(c, expected, execute)
	if err != nil {
		return nil, err
	}
	return &SchemaVersionResult{SchemaVersion: version, ETag: resp.Header.Get("ETag")}, nil
}

// paginate yields every result of every page, following the next cursor until
// there is none. An error on a later page is yielded after the earlier pages'
// results.
func paginate[T any](fetch func(cursor string) ([]T, *string, error)) iter.Seq2[*T, error] {
	return func(yield func(*T, error) bool) {
		cursor := ""
		for {
			results, next, err := fetch(cursor)
			if err != nil {
				yield(nil, err)
				return
			}
			for i := range results {
				if !yield(&results[i], nil) {
					return
				}
			}
			if next == nil {
				return
			}
			cursor = *next
		}
	}
}

// ListDefinitions returns one page of the team's event definitions.
func (s *Schemas) ListDefinitions(ctx context.Context, params PageParams) (*DefinitionPage, error) {
	request := s.api().EventDefinitionsList(s.client.authorize(ctx))
	if params.Cursor != "" {
		request = request.Cursor(params.Cursor)
	}
	if params.Limit != 0 {
		request = request.Limit(int32(params.Limit))
	}
	return run(ctx, s.client, func() (*DefinitionPage, error) {
		page, _, err := attempt(s.client, http.StatusOK, request.Execute)
		if err != nil {
			return nil, err
		}
		return &DefinitionPage{Results: page.Results, NextCursor: cursorFromNext(page.Next)}, nil
	})
}

// IterateDefinitions lazily walks every page of event definitions. An error
// on a later page is yielded after the definitions of the earlier pages.
func (s *Schemas) IterateDefinitions(ctx context.Context, params PageParams) iter.Seq2[*EventDefinition, error] {
	return paginate(func(cursor string) ([]EventDefinition, *string, error) {
		page, err := s.ListDefinitions(ctx, PageParams{Limit: params.Limit, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		return page.Results, page.NextCursor, nil
	})
}

// GetDefinition returns one event definition.
func (s *Schemas) GetDefinition(ctx context.Context, action string) (*EventDefinition, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().EventDefinitionsRetrieve(s.client.authorize(ctx), action)
	return run(ctx, s.client, func() (*EventDefinition, error) {
		definition, _, err := attempt(s.client, http.StatusOK, request.Execute)
		return definition, err
	})
}

// CreateDefinition defines an action; only the members input sets are sent.
func (s *Schemas) CreateDefinition(ctx context.Context, input DefinitionInput) (*EventDefinition, error) {
	if err := requireAction(input.Action); err != nil {
		return nil, err
	}
	request := s.api().EventDefinitionsCreate(s.client.authorize(ctx)).
		EventDefinitionWriteRequest(core.EventDefinitionWriteRequest{
			Action:             input.Action,
			Description:        input.Description,
			AllowedTargetTypes: input.AllowedTargetTypes,
			IsActive:           input.IsActive,
		})
	return run(ctx, s.client, func() (*EventDefinition, error) {
		definition, _, err := attempt(s.client, http.StatusCreated, request.Execute)
		return definition, err
	})
}

// UpdateDefinition changes a definition; only the members update sets are
// sent, and NewAction renames the action.
func (s *Schemas) UpdateDefinition(ctx context.Context, action string, update DefinitionUpdate) (*EventDefinition, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().EventDefinitionsUpdate(s.client.authorize(ctx), action).
		PatchedEventDefinitionWriteRequest(core.PatchedEventDefinitionWriteRequest{
			Action:             update.NewAction,
			Description:        update.Description,
			AllowedTargetTypes: update.AllowedTargetTypes,
			IsActive:           update.IsActive,
		})
	return run(ctx, s.client, func() (*EventDefinition, error) {
		definition, _, err := attempt(s.client, http.StatusOK, request.Execute)
		return definition, err
	})
}

// DeleteDefinition deletes an event definition.
func (s *Schemas) DeleteDefinition(ctx context.Context, action string) error {
	if err := requireAction(action); err != nil {
		return err
	}
	request := s.api().EventDefinitionsDestroy(s.client.authorize(ctx), action)
	_, err := run(ctx, s.client, func() (struct{}, error) {
		resp, err := request.Execute()
		return struct{}{}, s.client.check(resp, err, http.StatusNoContent)
	})
	return err
}

// ListVersions returns one page of an action's schema versions, newest first.
func (s *Schemas) ListVersions(ctx context.Context, action string, params PageParams) (*SchemaVersionPage, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsList(s.client.authorize(ctx), action)
	if params.Cursor != "" {
		request = request.Cursor(params.Cursor)
	}
	if params.Limit != 0 {
		request = request.Limit(int32(params.Limit))
	}
	return run(ctx, s.client, func() (*SchemaVersionPage, error) {
		page, _, err := attempt(s.client, http.StatusOK, request.Execute)
		if err != nil {
			return nil, err
		}
		return &SchemaVersionPage{Results: page.Results, NextCursor: cursorFromNext(page.Next)}, nil
	})
}

// IterateVersions lazily walks every page of an action's schema versions. An
// error on a later page is yielded after the versions of the earlier pages.
func (s *Schemas) IterateVersions(ctx context.Context, action string, params PageParams) iter.Seq2[*EventSchemaVersion, error] {
	return paginate(func(cursor string) ([]EventSchemaVersion, *string, error) {
		page, err := s.ListVersions(ctx, action, PageParams{Limit: params.Limit, Cursor: cursor})
		if err != nil {
			return nil, nil, err
		}
		return page.Results, page.NextCursor, nil
	})
}

// GetVersion returns one schema version with its ETag.
func (s *Schemas) GetVersion(ctx context.Context, action string, version int) (*SchemaVersionResult, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsRetrieve(s.client.authorize(ctx), action, int32(version))
	return run(ctx, s.client, func() (*SchemaVersionResult, error) {
		return versionAttempt(s.client, http.StatusOK, request.Execute)
	})
}

// CreateVersion creates a draft from a JSON Schema document. It is sent once:
// it is never retried.
func (s *Schemas) CreateVersion(ctx context.Context, action string, schema map[string]any) (*SchemaVersionResult, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsCreate(s.client.authorize(ctx), action).
		EventSchemaDocumentRequest(core.EventSchemaDocumentRequest{Schema: document(schema)})
	return runAttempts(ctx, s.client, 0, func() (*SchemaVersionResult, error) {
		return versionAttempt(s.client, http.StatusCreated, request.Execute)
	})
}

// UpdateVersion replaces a draft's document. A non-empty ifMatch (an ETag
// you read) makes a concurrent edit fail with an ApiError of status 412
// instead of being overwritten; "" sends no If-Match header.
func (s *Schemas) UpdateVersion(ctx context.Context, action string, version int, schema map[string]any, ifMatch string) (*SchemaVersionResult, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsUpdate(s.client.authorize(ctx), action, int32(version)).
		EventSchemaDocumentRequest(core.EventSchemaDocumentRequest{Schema: document(schema)})
	if ifMatch != "" {
		request = request.IfMatch(ifMatch)
	}
	return run(ctx, s.client, func() (*SchemaVersionResult, error) {
		return versionAttempt(s.client, http.StatusOK, request.Execute)
	})
}

// DeleteVersion deletes a draft; a published version fails with an ApiError of
// status 409.
func (s *Schemas) DeleteVersion(ctx context.Context, action string, version int) error {
	if err := requireAction(action); err != nil {
		return err
	}
	request := s.api().SchemaVersionsDestroy(s.client.authorize(ctx), action, int32(version))
	_, err := run(ctx, s.client, func() (struct{}, error) {
		resp, err := request.Execute()
		return struct{}{}, s.client.check(resp, err, http.StatusNoContent)
	})
	return err
}

// PublishVersion publishes a draft: it becomes immutable and validates ingest.
func (s *Schemas) PublishVersion(ctx context.Context, action string, version int) (*SchemaVersionResult, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsPublish(s.client.authorize(ctx), action, int32(version))
	return run(ctx, s.client, func() (*SchemaVersionResult, error) {
		return versionAttempt(s.client, http.StatusOK, request.Execute)
	})
}

// DeprecateVersion starts the migration window of a superseded version.
func (s *Schemas) DeprecateVersion(ctx context.Context, action string, version int) (*SchemaVersionResult, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsDeprecate(s.client.authorize(ctx), action, int32(version))
	return run(ctx, s.client, func() (*SchemaVersionResult, error) {
		return versionAttempt(s.client, http.StatusOK, request.Execute)
	})
}

// CheckSchema dry-runs a document against the action's newest stored events;
// nothing is stored.
func (s *Schemas) CheckSchema(ctx context.Context, action string, schema map[string]any) (*SchemaCheck, error) {
	if err := requireAction(action); err != nil {
		return nil, err
	}
	request := s.api().SchemaVersionsCheck(s.client.authorize(ctx), action).
		EventSchemaDocumentRequest(core.EventSchemaDocumentRequest{Schema: document(schema)})
	return run(ctx, s.client, func() (*SchemaCheck, error) {
		check, _, err := attempt(s.client, http.StatusOK, request.Execute)
		return check, err
	})
}

// ListStandardPacks returns the standard catalog: every pack and its actions.
func (s *Schemas) ListStandardPacks(ctx context.Context) (*StandardEventCatalog, error) {
	request := s.api().StandardEventsList(s.client.authorize(ctx))
	return run(ctx, s.client, func() (*StandardEventCatalog, error) {
		catalog, _, err := attempt(s.client, http.StatusOK, request.Execute)
		return catalog, err
	})
}

// InstallStandardPacks installs packs by slug. The slugs are forwarded
// unchanged and the server judges them.
func (s *Schemas) InstallStandardPacks(ctx context.Context, packs []string) (*StandardPackInstallResult, error) {
	if packs == nil {
		packs = []string{}
	}
	request := s.api().StandardEventsInstall(s.client.authorize(ctx)).
		StandardPackInstallRequest(core.StandardPackInstallRequest{Packs: packs})
	return run(ctx, s.client, func() (*StandardPackInstallResult, error) {
		result, _, err := attempt(s.client, http.StatusOK, request.Execute)
		return result, err
	})
}

// document is the schema member of a request: a nil map is sent as {}, never
// as null.
func document(schema map[string]any) map[string]interface{} {
	if schema == nil {
		return map[string]interface{}{}
	}
	return schema
}
