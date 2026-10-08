//! The schema operations: event definitions, their schema versions and the
//! standard packs.

use std::collections::VecDeque;
use std::future::Future;

use futures_util::stream::{self, Stream};
use reqwest::RequestBuilder;
use serde::de::DeserializeOwned;
use serde_json::{Map, Value};

use crate::client::Santati;
use crate::error::Error;
use crate::http::{cursor_from_url, encode_segment, send, RawResponse};
use crate::models;
use crate::retry::{run as run_with_retries, RetryPolicy};
use crate::types::{
    DefinitionInput, DefinitionPage, DefinitionUpdate, PageParams, SchemaVersionPage,
    SchemaVersionResult,
};

const DEFINITIONS_PATH: &str = "/api/v0/event-definitions/";
const STANDARD_PATH: &str = "/api/v0/standard-events/";

/// The client's schema operations, borrowed from a [`Santati`].
///
/// Every operation but [`Schemas::create_version`] retries like the events
/// operations; `create_version` is sent once, because a repeat would create a
/// second draft. An empty `action` fails with [`Error::Validation`] (field
/// `action`) before any request; everything else is forwarded as given and
/// the server decides.
#[derive(Clone, Copy)]
pub struct Schemas<'a> {
    client: &'a Santati,
}

impl<'a> Schemas<'a> {
    pub(crate) fn new(client: &'a Santati) -> Schemas<'a> {
        Schemas { client }
    }

    /// Fetch one page of the team's event definitions.
    pub async fn list_definitions(&self, params: PageParams) -> Result<DefinitionPage, Error> {
        let query = page_query(&params);
        let page: models::PaginatedEventDefinitionList = self
            .call(
                true,
                200,
                || self.get(DEFINITIONS_PATH.to_string(), &query),
                decode,
            )
            .await?;
        Ok(DefinitionPage {
            results: page.results,
            next_cursor: next_cursor(page.next),
        })
    }

    /// Walk every page of event definitions: a lazy stream that starts on the
    /// first poll. An error on a later page is yielded after the earlier
    /// definitions.
    pub fn iterate_definitions(
        self,
        params: PageParams,
    ) -> impl Stream<Item = Result<models::EventDefinition, Error>> + 'a {
        let limit = params.limit;
        pages(move |cursor| async move {
            let page = self.list_definitions(PageParams { limit, cursor }).await?;
            Ok((page.results, page.next_cursor))
        })
    }

    /// Fetch one event definition.
    pub async fn get_definition(&self, action: &str) -> Result<models::EventDefinition, Error> {
        require_action(action)?;
        let path = definition_path(action);
        self.call(true, 200, || self.get(path.clone(), &[]), decode)
            .await
    }

    /// Define an action; only the members that are `Some` are sent.
    pub async fn create_definition(
        &self,
        definition: DefinitionInput,
    ) -> Result<models::EventDefinition, Error> {
        require_action(&definition.action)?;
        let body = models::EventDefinitionWriteRequest {
            action: definition.action,
            description: definition.description,
            allowed_target_types: definition.allowed_target_types,
            is_active: definition.is_active,
        };
        self.call(
            true,
            201,
            || {
                self.request(reqwest::Method::POST, DEFINITIONS_PATH.to_string())
                    .json(&body)
            },
            decode,
        )
        .await
    }

    /// Change a definition; only the members that are `Some` are sent, and
    /// `new_action` renames the action.
    pub async fn update_definition(
        &self,
        action: &str,
        update: DefinitionUpdate,
    ) -> Result<models::EventDefinition, Error> {
        require_action(action)?;
        let path = definition_path(action);
        let body = models::PatchedEventDefinitionWriteRequest {
            action: update.new_action,
            description: update.description,
            allowed_target_types: update.allowed_target_types,
            is_active: update.is_active,
        };
        self.call(
            true,
            200,
            || {
                self.request(reqwest::Method::PATCH, path.clone())
                    .json(&body)
            },
            decode,
        )
        .await
    }

    /// Delete an event definition.
    pub async fn delete_definition(&self, action: &str) -> Result<(), Error> {
        require_action(action)?;
        let path = definition_path(action);
        self.call(
            true,
            204,
            || self.request(reqwest::Method::DELETE, path.clone()),
            |_| Ok(()),
        )
        .await
    }

    /// Fetch one page of an action's schema versions, newest first.
    pub async fn list_versions(
        &self,
        action: &str,
        params: PageParams,
    ) -> Result<SchemaVersionPage, Error> {
        require_action(action)?;
        let path = versions_path(action);
        let query = page_query(&params);
        let page: models::PaginatedEventSchemaVersionList = self
            .call(true, 200, || self.get(path.clone(), &query), decode)
            .await?;
        Ok(SchemaVersionPage {
            results: page.results,
            next_cursor: next_cursor(page.next),
        })
    }

    /// Walk every page of an action's schema versions: a lazy stream that
    /// starts on the first poll. An error on a later page is yielded after
    /// the earlier versions.
    pub fn iterate_versions(
        self,
        action: impl Into<String>,
        params: PageParams,
    ) -> impl Stream<Item = Result<models::EventSchemaVersion, Error>> + 'a {
        let action = action.into();
        let limit = params.limit;
        pages(move |cursor| {
            let action = action.clone();
            async move {
                let page = self
                    .list_versions(&action, PageParams { limit, cursor })
                    .await?;
                Ok((page.results, page.next_cursor))
            }
        })
    }

    /// Fetch one schema version and its `ETag`.
    pub async fn get_version(
        &self,
        action: &str,
        version: i32,
    ) -> Result<SchemaVersionResult, Error> {
        require_action(action)?;
        let path = version_path(action, version);
        self.call(true, 200, || self.get(path.clone(), &[]), versioned)
            .await
    }

    /// Create a draft from a JSON Schema document. Sent once: it is never
    /// retried.
    pub async fn create_version(
        &self,
        action: &str,
        schema: Map<String, Value>,
    ) -> Result<SchemaVersionResult, Error> {
        require_action(action)?;
        let path = versions_path(action);
        let body = document(schema);
        self.call(
            false,
            201,
            || {
                self.request(reqwest::Method::POST, path.clone())
                    .json(&body)
            },
            versioned,
        )
        .await
    }

    /// Replace a draft's document. With `if_match` (an `ETag` you read) a
    /// concurrent edit fails with [`Error::Api`] of status 412 instead of
    /// being overwritten.
    pub async fn update_version(
        &self,
        action: &str,
        version: i32,
        schema: Map<String, Value>,
        if_match: Option<&str>,
    ) -> Result<SchemaVersionResult, Error> {
        require_action(action)?;
        let path = version_path(action, version);
        let body = document(schema);
        self.call(
            true,
            200,
            || {
                let request = self.request(reqwest::Method::PUT, path.clone()).json(&body);
                match if_match {
                    Some(etag) => request.header(reqwest::header::IF_MATCH, etag),
                    None => request,
                }
            },
            versioned,
        )
        .await
    }

    /// Delete a draft; a published version fails with [`Error::Api`] of
    /// status 409.
    pub async fn delete_version(&self, action: &str, version: i32) -> Result<(), Error> {
        require_action(action)?;
        let path = version_path(action, version);
        self.call(
            true,
            204,
            || self.request(reqwest::Method::DELETE, path.clone()),
            |_| Ok(()),
        )
        .await
    }

    /// Publish a draft: it becomes immutable and validates ingest.
    pub async fn publish_version(
        &self,
        action: &str,
        version: i32,
    ) -> Result<SchemaVersionResult, Error> {
        require_action(action)?;
        let path = format!("{}publish/", version_path(action, version));
        self.call(
            true,
            200,
            || self.request(reqwest::Method::POST, path.clone()),
            versioned,
        )
        .await
    }

    /// Start the migration window of a superseded version.
    pub async fn deprecate_version(
        &self,
        action: &str,
        version: i32,
    ) -> Result<SchemaVersionResult, Error> {
        require_action(action)?;
        let path = format!("{}deprecate/", version_path(action, version));
        self.call(
            true,
            200,
            || self.request(reqwest::Method::POST, path.clone()),
            versioned,
        )
        .await
    }

    /// Dry-run a document against the action's newest stored events; nothing
    /// is stored.
    pub async fn check_schema(
        &self,
        action: &str,
        schema: Map<String, Value>,
    ) -> Result<models::SchemaCheck, Error> {
        require_action(action)?;
        let path = format!("{}check/", versions_path(action));
        let body = document(schema);
        self.call(
            true,
            200,
            || {
                self.request(reqwest::Method::POST, path.clone())
                    .json(&body)
            },
            decode,
        )
        .await
    }

    /// The standard catalog: every pack and its actions.
    pub async fn list_standard_packs(&self) -> Result<models::StandardEventCatalog, Error> {
        self.call(
            true,
            200,
            || self.get(STANDARD_PATH.to_string(), &[]),
            decode,
        )
        .await
    }

    /// Install packs by slug. The slugs are forwarded unchanged and the server
    /// judges them.
    pub async fn install_standard_packs(
        &self,
        packs: Vec<String>,
    ) -> Result<models::StandardPackInstallResult, Error> {
        let body = models::StandardPackInstallRequest { packs };
        self.call(
            true,
            200,
            || {
                self.request(reqwest::Method::POST, format!("{STANDARD_PATH}install/"))
                    .json(&body)
            },
            decode,
        )
        .await
    }

    fn request(&self, method: reqwest::Method, path: String) -> RequestBuilder {
        self.client
            .http()
            .request(method, self.client.endpoint(&path))
    }

    fn get(&self, path: String, query: &[(&'static str, String)]) -> RequestBuilder {
        let request = self.request(reqwest::Method::GET, path);
        if query.is_empty() {
            request
        } else {
            request.query(query)
        }
    }

    /// One operation: `build` makes the identical request for every attempt,
    /// the `expected` 2xx status is the only success, and `result` reads the
    /// answer. Retried like the events operations unless `retries` is false.
    async fn call<T>(
        &self,
        retries: bool,
        expected: u16,
        build: impl Fn() -> RequestBuilder,
        result: impl Fn(&RawResponse) -> Result<T, Error>,
    ) -> Result<T, Error> {
        let policy = if retries {
            self.client.retry_policy()
        } else {
            RetryPolicy {
                max_retries: 0,
                ..self.client.retry_policy()
            }
        };
        run_with_retries(policy, || async {
            let response = send(build()).await?;
            if response.status == expected {
                result(&response)
            } else if (200..300).contains(&response.status) {
                Err(Error::api(response.status))
            } else {
                Err(Error::from_http(
                    response.status,
                    &response.headers,
                    &response.body,
                ))
            }
        })
        .await
    }
}

fn require_action(action: &str) -> Result<(), Error> {
    if action.is_empty() {
        return Err(Error::validation("action", "action must not be empty"));
    }
    Ok(())
}

fn definition_path(action: &str) -> String {
    format!("{DEFINITIONS_PATH}{}/", encode_segment(action))
}

fn versions_path(action: &str) -> String {
    format!("{}schema-versions/", definition_path(action))
}

fn version_path(action: &str, version: i32) -> String {
    format!("{}{version}/", versions_path(action))
}

/// The page selectors as query pairs in spec order; absent ones are not sent.
fn page_query(params: &PageParams) -> Vec<(&'static str, String)> {
    let mut pairs = Vec::new();
    if let Some(cursor) = &params.cursor {
        pairs.push(("cursor", cursor.clone()));
    }
    if let Some(limit) = params.limit {
        pairs.push(("limit", limit.to_string()));
    }
    pairs
}

fn document(schema: Map<String, Value>) -> models::EventSchemaDocumentRequest {
    models::EventSchemaDocumentRequest {
        schema: schema.into_iter().collect(),
    }
}

/// A 2xx body decoded with the generated model, or [`Error::Api`].
fn decode<T: DeserializeOwned>(response: &RawResponse) -> Result<T, Error> {
    serde_json::from_slice(&response.body).map_err(|_| Error::api(response.status))
}

fn versioned(response: &RawResponse) -> Result<SchemaVersionResult, Error> {
    Ok(SchemaVersionResult {
        schema_version: decode(response)?,
        etag: response
            .headers
            .get(reqwest::header::ETAG)
            .and_then(|value| value.to_str().ok())
            .map(str::to_string),
    })
}

fn next_cursor(next: Option<Option<String>>) -> Option<String> {
    next.flatten().as_deref().and_then(cursor_from_url)
}

/// A lazy stream over a paged operation: `fetch(cursor)` answers one page's
/// results and the next cursor, and the stream ends after the page whose next
/// cursor is `None`.
fn pages<'a, T, F, Fut>(fetch: F) -> impl Stream<Item = Result<T, Error>> + 'a
where
    T: 'a,
    F: Fn(Option<String>) -> Fut + Clone + 'a,
    Fut: Future<Output = Result<(Vec<T>, Option<String>), Error>> + 'a,
{
    let state = (VecDeque::<T>::new(), None::<String>, false);
    stream::try_unfold(state, move |(mut pending, mut cursor, mut done)| {
        let fetch = fetch.clone();
        async move {
            loop {
                if let Some(item) = pending.pop_front() {
                    return Ok(Some((item, (pending, cursor, done))));
                }
                if done {
                    return Ok(None);
                }
                let (results, next) = fetch(cursor.take()).await?;
                done = next.is_none();
                cursor = next;
                pending = results.into();
            }
        }
    })
}
