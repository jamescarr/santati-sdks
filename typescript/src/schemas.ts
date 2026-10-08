import {
  EventDefinitionFromJSON,
  EventDefinitionsApi,
  EventSchemaVersionFromJSON,
  PaginatedEventDefinitionListFromJSON,
  PaginatedEventSchemaVersionListFromJSON,
  SchemaCheckFromJSON,
  StandardEventCatalogFromJSON,
  StandardPackInstallResultFromJSON,
} from "./core/index.js";
import type {
  EventDefinition,
  EventDefinitionWriteRequest,
  PatchedEventDefinitionWriteRequest,
  EventSchemaVersion,
  SchemaCheck,
  StandardEventCatalog,
  StandardPackInstallResult,
} from "./core/index.js";
import type { ClientConfig } from "./client.js";
import { ValidationError } from "./errors.js";
import {
  coreConfiguration,
  decodeJson,
  nextCursor,
  requestInit,
  send,
  unexpected,
} from "./http.js";
import type { RawResponse } from "./http.js";
import { withRetries } from "./retry.js";
import type {
  DefinitionInput,
  DefinitionPage,
  DefinitionUpdate,
  IteratePageParams,
  PageParams,
  SchemaVersionPage,
  SchemaVersionResult,
} from "./types.js";

/** A JSON Schema document: forwarded as given. */
type SchemaDocument = Record<string, unknown>;

function requireAction(action: string): void {
  if (typeof action !== "string" || action.length === 0) {
    throw new ValidationError("action must be a non-empty string", { field: "action" });
  }
}

/** The page parameters, in the generated request's own camelCase; absent members are not sent. */
function pageRequest(params: PageParams): PageParams {
  const request: PageParams = {};
  if (params.cursor != null) request.cursor = params.cursor;
  if (params.limit != null) request.limit = params.limit;
  return request;
}

function versionResult(raw: RawResponse): SchemaVersionResult {
  return {
    schemaVersion: decodeJson(raw, EventSchemaVersionFromJSON),
    etag: raw.headers.get("etag"),
  };
}

/**
 * The `schemas` resource: event definitions, their schema versions and the
 * standard packs. It shares the client's key, base URL and headers through its
 * own generated configuration/client pair.
 *
 * Every operation but `createVersion` retries like `events`; `createVersion`
 * is sent once, because a repeat would create a second draft. An empty
 * `action` rejects with `ValidationError` before any request; everything else
 * is forwarded as given and the server decides.
 */
export class Schemas {
  private readonly api: EventDefinitionsApi;
  private readonly config: ClientConfig;

  constructor(config: ClientConfig) {
    this.config = config;
    this.api = new EventDefinitionsApi(coreConfiguration(config));
  }

  /** One page of the team's event definitions. */
  async listDefinitions(params: PageParams = {}): Promise<DefinitionPage> {
    const raw = await this.run(
      (init) => this.api.eventDefinitionsListRaw(pageRequest(params), init),
      200,
    );
    const page = decodeJson(raw, PaginatedEventDefinitionListFromJSON);
    return { results: page.results, nextCursor: nextCursor(raw, page.next) };
  }

  /** Every event definition, page by page; a later failure surfaces after earlier yields. */
  async *iterateDefinitions(
    params: IteratePageParams = {},
  ): AsyncGenerator<EventDefinition, void, undefined> {
    let cursor: string | null = null;
    for (;;) {
      const page = await this.listDefinitions(cursor === null ? params : { ...params, cursor });
      yield* page.results;
      if (page.nextCursor === null) return;
      cursor = page.nextCursor;
    }
  }

  /** One event definition. */
  async getDefinition(action: string): Promise<EventDefinition> {
    requireAction(action);
    const raw = await this.run((init) => this.api.eventDefinitionsRetrieveRaw({ action }, init), 200);
    return decodeJson(raw, EventDefinitionFromJSON);
  }

  /** Defines an action; only the members you supply are sent. */
  async createDefinition(definition: DefinitionInput): Promise<EventDefinition> {
    requireAction(definition.action);
    const eventDefinitionWriteRequest: EventDefinitionWriteRequest = { action: definition.action };
    if (definition.description != null) eventDefinitionWriteRequest.description = definition.description;
    if (definition.allowedTargetTypes != null) {
      eventDefinitionWriteRequest.allowedTargetTypes = definition.allowedTargetTypes;
    }
    if (definition.isActive != null) eventDefinitionWriteRequest.isActive = definition.isActive;
    const raw = await this.run(
      (init) => this.api.eventDefinitionsCreateRaw({ eventDefinitionWriteRequest }, init),
      201,
    );
    return decodeJson(raw, EventDefinitionFromJSON);
  }

  /** Changes a definition; only the members you supply are sent, `newAction` renames it. */
  async updateDefinition(action: string, changes: DefinitionUpdate): Promise<EventDefinition> {
    requireAction(action);
    const patchedEventDefinitionWriteRequest: PatchedEventDefinitionWriteRequest = {};
    if (changes.newAction != null) patchedEventDefinitionWriteRequest.action = changes.newAction;
    if (changes.description != null) patchedEventDefinitionWriteRequest.description = changes.description;
    if (changes.allowedTargetTypes != null) {
      patchedEventDefinitionWriteRequest.allowedTargetTypes = changes.allowedTargetTypes;
    }
    if (changes.isActive != null) patchedEventDefinitionWriteRequest.isActive = changes.isActive;
    const raw = await this.run(
      (init) =>
        this.api.eventDefinitionsUpdateRaw({ action, patchedEventDefinitionWriteRequest }, init),
      200,
    );
    return decodeJson(raw, EventDefinitionFromJSON);
  }

  /** Deletes an event definition. */
  async deleteDefinition(action: string): Promise<void> {
    requireAction(action);
    await this.run((init) => this.api.eventDefinitionsDestroyRaw({ action }, init), 204);
  }

  /** One page of an action's schema versions, newest first. */
  async listVersions(action: string, params: PageParams = {}): Promise<SchemaVersionPage> {
    requireAction(action);
    const raw = await this.run(
      (init) => this.api.schemaVersionsListRaw({ action, ...pageRequest(params) }, init),
      200,
    );
    const page = decodeJson(raw, PaginatedEventSchemaVersionListFromJSON);
    return { results: page.results, nextCursor: nextCursor(raw, page.next) };
  }

  /** Every schema version of an action, page by page; a later failure surfaces after earlier yields. */
  async *iterateVersions(
    action: string,
    params: IteratePageParams = {},
  ): AsyncGenerator<EventSchemaVersion, void, undefined> {
    let cursor: string | null = null;
    for (;;) {
      const page = await this.listVersions(action, cursor === null ? params : { ...params, cursor });
      yield* page.results;
      if (page.nextCursor === null) return;
      cursor = page.nextCursor;
    }
  }

  /** One schema version with its `ETag`. */
  async getVersion(action: string, version: number): Promise<SchemaVersionResult> {
    requireAction(action);
    const raw = await this.run(
      (init) => this.api.schemaVersionsRetrieveRaw({ action, version }, init),
      200,
    );
    return versionResult(raw);
  }

  /** Creates a draft from a JSON Schema document. Sent once: it is never retried. */
  async createVersion(action: string, schema: SchemaDocument): Promise<SchemaVersionResult> {
    requireAction(action);
    const raw = await this.run(
      (init) =>
        this.api.schemaVersionsCreateRaw({ action, eventSchemaDocumentRequest: { schema } }, init),
      201,
      false,
    );
    return versionResult(raw);
  }

  /**
   * Replaces a draft's document. `ifMatch` (an `ETag` you read) makes a
   * concurrent edit reject with `ApiError` 412 instead of being overwritten.
   */
  async updateVersion(
    action: string,
    version: number,
    schema: SchemaDocument,
    options: { ifMatch?: string } = {},
  ): Promise<SchemaVersionResult> {
    requireAction(action);
    const raw = await this.run(
      (init) =>
        this.api.schemaVersionsUpdateRaw(
          { action, version, eventSchemaDocumentRequest: { schema }, ifMatch: options.ifMatch },
          init,
        ),
      200,
    );
    return versionResult(raw);
  }

  /** Deletes a draft; a published version rejects with `ApiError` 409. */
  async deleteVersion(action: string, version: number): Promise<void> {
    requireAction(action);
    await this.run((init) => this.api.schemaVersionsDestroyRaw({ action, version }, init), 204);
  }

  /** Publishes a draft: it becomes immutable and validates ingest. */
  async publishVersion(action: string, version: number): Promise<SchemaVersionResult> {
    requireAction(action);
    const raw = await this.run(
      (init) => this.api.schemaVersionsPublishRaw({ action, version }, init),
      200,
    );
    return versionResult(raw);
  }

  /** Starts the migration window of a superseded version. */
  async deprecateVersion(action: string, version: number): Promise<SchemaVersionResult> {
    requireAction(action);
    const raw = await this.run(
      (init) => this.api.schemaVersionsDeprecateRaw({ action, version }, init),
      200,
    );
    return versionResult(raw);
  }

  /** Dry-runs a document against the action's newest stored events; nothing is stored. */
  async checkSchema(action: string, schema: SchemaDocument): Promise<SchemaCheck> {
    requireAction(action);
    const raw = await this.run(
      (init) =>
        this.api.schemaVersionsCheckRaw({ action, eventSchemaDocumentRequest: { schema } }, init),
      200,
    );
    return decodeJson(raw, SchemaCheckFromJSON);
  }

  /** The standard catalog: every pack and its actions. */
  async listStandardPacks(): Promise<StandardEventCatalog> {
    const raw = await this.run((init) => this.api.standardEventsListRaw(init), 200);
    return decodeJson(raw, StandardEventCatalogFromJSON);
  }

  /** Installs packs by slug; the slugs are forwarded unchanged and the server judges them. */
  async installStandardPacks(packs: string[]): Promise<StandardPackInstallResult> {
    const raw = await this.run(
      (init) => this.api.standardEventsInstallRaw({ standardPackInstallRequest: { packs } }, init),
      200,
    );
    return decodeJson(raw, StandardPackInstallResultFromJSON);
  }

  /**
   * One operation: each attempt makes the generated call with a fresh timeout
   * signal, and the `expected` 2xx status is the only success. Retried like
   * `events` unless `retries` is false.
   */
  private async run(
    call: (init: RequestInit) => Promise<{ raw: Response }>,
    expected: number,
    retries = true,
  ): Promise<RawResponse> {
    const attempt = () => send(() => call(requestInit(this.config.timeoutMs)));
    const raw = retries ? await withRetries(attempt, this.config) : await attempt();
    if (raw.status !== expected) throw unexpected(raw);
    return raw;
  }
}
