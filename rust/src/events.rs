//! The event operations: `emit`, `emit_batch`, `list` and `iterate`.

use std::collections::VecDeque;

use futures_util::stream::{self, Stream};
use reqwest::header::HeaderMap;
use reqwest::{RequestBuilder, Response};

use crate::client::Santati;
use crate::error::Error;
use crate::models;
use crate::outbox;
use crate::retry::{run as run_with_retries, RetryPolicy};
use crate::types::{
    ActorInput, BatchItem, BatchItemError, BatchResult, BatchStatus, EmitResult, EventInput,
    EventPage, ListParams, TargetInput,
};
use crate::AuditEvent;

/// Every request goes here, under the client's base URL.
const EVENTS_PATH: &str = "/api/v0/events/";

/// The client's event operations, borrowed from a [`Santati`].
#[derive(Clone, Copy)]
pub struct Events<'a> {
    client: &'a Santati,
}

impl<'a> Events<'a> {
    pub(crate) fn new(client: &'a Santati) -> Events<'a> {
        Events { client }
    }

    /// Emit one event.
    ///
    /// The envelope carries exactly the supplied members; `trail` falls back to
    /// the client's, and `idempotency_key` to a freshly generated UUIDv4. A
    /// `201` is a new event and a `200` a replay: see [`EmitResult::duplicate`].
    ///
    /// With [`Builder::outbox`](crate::Builder::outbox), the event is stored and
    /// the call returns at once with `queued` set; it never makes a request.
    pub async fn emit(&self, event: EventInput) -> Result<EmitResult, Error> {
        let envelope = self.envelope(&event, "")?;
        let idempotency_key = envelope.idempotency_key.clone().unwrap_or_default();
        if let Some(state) = self.client.outbox_state() {
            let stored = EventInput {
                trail: Some(envelope.trail),
                idempotency_key: Some(idempotency_key.clone()),
                data: event.data.filter(|data| !data.is_null()),
                ..event
            };
            outbox::enqueue(self.client, state, stored).await?;
            return Ok(EmitResult {
                event: None,
                duplicate: false,
                idempotency_key,
                queued: true,
            });
        }
        let body = models::EventIngestRequest::EventEnvelopeRequest(envelope);
        let policy = self.client.retry_policy();
        run_with_retries(policy, || self.attempt_emit(&body, &idempotency_key)).await
    }

    /// Emit a batch of events in one request.
    ///
    /// One idempotency key is generated per event that does not have one, and a
    /// `207` (some items rejected) is a [`BatchResult`], not an error.
    pub async fn emit_batch(&self, events: Vec<EventInput>) -> Result<BatchResult, Error> {
        self.emit_batch_with_status(events, true)
            .await
            .map(|(batch, _status)| batch)
    }

    /// [`Events::emit_batch`] that also returns the response's HTTP status
    /// (202 or 207). With `retries` false the request is sent once, which is
    /// how the outbox sends: a retryable failure releases the batch for a
    /// later pass.
    pub(crate) async fn emit_batch_with_status(
        &self,
        events: Vec<EventInput>,
        retries: bool,
    ) -> Result<(BatchResult, u16), Error> {
        if events.is_empty() {
            return Err(Error::validation("events", "events must not be empty"));
        }
        let mut envelopes = Vec::with_capacity(events.len());
        for (index, event) in events.iter().enumerate() {
            envelopes.push(self.envelope(event, &format!("events[{index}]."))?);
        }
        let body = models::EventIngestRequest::EventBatchRequest(models::EventBatchRequest {
            events: envelopes,
        });
        let policy = if retries {
            self.client.retry_policy()
        } else {
            RetryPolicy {
                max_retries: 0,
                ..self.client.retry_policy()
            }
        };
        run_with_retries(policy, || self.attempt_batch(&body)).await
    }

    /// Fetch one page of events.
    ///
    /// The client's default trail is never applied to reads.
    pub async fn list(&self, params: ListParams) -> Result<EventPage, Error> {
        let query = query_pairs(&params);
        let policy = self.client.retry_policy();
        run_with_retries(policy, || self.attempt_list(&query)).await
    }

    /// Walk every page of events: a lazy stream that starts on the first poll
    /// and keeps calling [`Events::list`] until `next_cursor` is `None`.
    ///
    /// An error on a later page is yielded after the earlier events.
    pub fn iterate(self, params: ListParams) -> impl Stream<Item = Result<AuditEvent, Error>> + 'a {
        let params = ListParams {
            cursor: None,
            ..params
        };
        let state = Pages {
            pending: VecDeque::new(),
            cursor: None,
            done: false,
            params,
        };
        stream::try_unfold(state, move |mut state| async move {
            loop {
                if let Some(event) = state.pending.pop_front() {
                    return Ok(Some((event, state)));
                }
                if state.done {
                    return Ok(None);
                }
                let mut params = state.params.clone();
                params.cursor = state.cursor.take();
                let page = self.list(params).await?;
                state.cursor = page.next_cursor;
                state.done = state.cursor.is_none();
                state.pending = page.results.into();
            }
        })
    }

    async fn attempt_emit(
        &self,
        body: &models::EventIngestRequest,
        idempotency_key: &str,
    ) -> Result<EmitResult, Error> {
        let response = send(self.post().json(body)).await?;
        match response.status {
            200 | 201 => {
                let event: models::AuditEvent = serde_json::from_slice(&response.body)
                    .map_err(|_| Error::api(response.status))?;
                Ok(EmitResult {
                    event: Some(event),
                    duplicate: response.status == 200,
                    idempotency_key: idempotency_key.to_string(),
                    queued: false,
                })
            }
            status if (200..300).contains(&status) => Err(Error::api(status)),
            status => Err(Error::from_http(status, &response.headers, &response.body)),
        }
    }

    async fn attempt_batch(
        &self,
        body: &models::EventIngestRequest,
    ) -> Result<(BatchResult, u16), Error> {
        let response = send(self.post().json(body)).await?;
        match response.status {
            202 | 207 => {
                let batch: models::EventBatchResult = serde_json::from_slice(&response.body)
                    .map_err(|_| Error::api(response.status))?;
                Ok((convert_batch(batch), response.status))
            }
            status if (200..300).contains(&status) => Err(Error::api(status)),
            status => Err(Error::from_http(status, &response.headers, &response.body)),
        }
    }

    async fn attempt_list(&self, query: &[(&'static str, String)]) -> Result<EventPage, Error> {
        let mut request = self.client.http().get(self.client.endpoint(EVENTS_PATH));
        if !query.is_empty() {
            request = request.query(query);
        }
        let response = send(request).await?;
        match response.status {
            200 => {
                let page: models::PaginatedAuditEventList =
                    serde_json::from_slice(&response.body).map_err(|_| Error::api(200))?;
                Ok(EventPage {
                    results: page.results,
                    next_cursor: page.next.flatten().as_deref().and_then(cursor_from_url),
                })
            }
            status if (200..300).contains(&status) => Err(Error::api(status)),
            status => Err(Error::from_http(status, &response.headers, &response.body)),
        }
    }

    fn post(&self) -> RequestBuilder {
        self.client.http().post(self.client.endpoint(EVENTS_PATH))
    }

    /// Validate one envelope locally and shape it for the wire.
    pub(crate) fn envelope(
        &self,
        input: &EventInput,
        prefix: &str,
    ) -> Result<models::EventEnvelopeRequest, Error> {
        if input.event.is_empty() {
            return Err(Error::validation(
                format!("{prefix}event"),
                "event must not be empty",
            ));
        }
        let trail = input
            .trail
            .as_deref()
            .filter(|trail| !trail.is_empty())
            .or_else(|| self.client.default_trail())
            .ok_or_else(|| {
                Error::validation(
                    format!("{prefix}trail"),
                    "a trail is required: set it on the event or on the client",
                )
            })?
            .to_string();
        let idempotency_key = input
            .idempotency_key
            .clone()
            .filter(|key| !key.is_empty())
            .unwrap_or_else(new_uuid);

        Ok(models::EventEnvelopeRequest {
            event: input.event.clone(),
            trail,
            created_at: input.created_at.clone(),
            organization_id: input.organization_id.clone(),
            idempotency_key: Some(idempotency_key),
            actor: input.actor.as_ref().map(actor_request),
            targets: input
                .targets
                .as_ref()
                .map(|targets| targets.iter().map(target_request).collect()),
            metadata: input.metadata.clone(),
            data: input.data.clone().filter(|data| !data.is_null()).map(Some),
            context: input.context.as_ref().map(|context| {
                context
                    .iter()
                    .map(|(key, value)| (key.clone(), value.clone()))
                    .collect()
            }),
        })
    }
}

/// The state [`stream::try_unfold`] threads from page to page.
struct Pages {
    pending: VecDeque<AuditEvent>,
    cursor: Option<String>,
    done: bool,
    params: ListParams,
}

/// One response, read to the end so the connection is reusable.
struct RawResponse {
    status: u16,
    headers: HeaderMap,
    body: Vec<u8>,
}

async fn send(request: RequestBuilder) -> Result<RawResponse, Error> {
    let response: Response = request
        .send()
        .await
        .map_err(|error| Error::transport(error.to_string()))?;
    let status = response.status().as_u16();
    let headers = response.headers().clone();
    let body = response
        .bytes()
        .await
        .map_err(|error| Error::transport(error.to_string()))?
        .to_vec();
    Ok(RawResponse {
        status,
        headers,
        body,
    })
}

/// The query parameters in spec order; absent filters are not sent.
fn query_pairs(params: &ListParams) -> Vec<(&'static str, String)> {
    let mut pairs = Vec::new();
    if let Some(value) = &params.actor_id {
        pairs.push(("actor_id", value.clone()));
    }
    if let Some(value) = &params.actor_type {
        pairs.push(("actor_type", value.clone()));
    }
    if let Some(value) = &params.created_after {
        pairs.push(("created_after", value.clone()));
    }
    if let Some(value) = &params.created_before {
        pairs.push(("created_before", value.clone()));
    }
    if let Some(value) = &params.cursor {
        pairs.push(("cursor", value.clone()));
    }
    if let Some(value) = &params.event {
        pairs.push(("event", value.clone()));
    }
    if let Some(value) = &params.event_prefix {
        pairs.push(("event_prefix", value.clone()));
    }
    if let Some(value) = params.limit {
        pairs.push(("limit", value.to_string()));
    }
    if let Some(value) = &params.organization_id {
        pairs.push(("organization_id", value.clone()));
    }
    if let Some(value) = &params.q {
        pairs.push(("q", value.clone()));
    }
    if let Some(value) = &params.sort {
        pairs.push(("sort", value.clone()));
    }
    if let Some(value) = &params.target_id {
        pairs.push(("target_id", value.clone()));
    }
    if let Some(value) = &params.target_type {
        pairs.push(("target_type", value.clone()));
    }
    if let Some(value) = &params.trail {
        pairs.push(("trail", value.clone()));
    }
    pairs
}

/// The decoded `cursor` of a page's `next` URL, when it carries one.
fn cursor_from_url(next: &str) -> Option<String> {
    let url = url::Url::parse(next).ok()?;
    url.query_pairs()
        .find(|(key, _)| key == "cursor")
        .map(|(_, value)| value.into_owned())
}

fn convert_batch(batch: models::EventBatchResult) -> BatchResult {
    BatchResult {
        accepted: batch.accepted,
        rejected: batch.rejected,
        results: batch
            .results
            .into_iter()
            .map(|item| BatchItem {
                index: item.index,
                status: match item.status {
                    models::event_batch_item_result::Status::Accepted => BatchStatus::Accepted,
                    models::event_batch_item_result::Status::Duplicate => BatchStatus::Duplicate,
                    models::event_batch_item_result::Status::Rejected => BatchStatus::Rejected,
                },
                id: item.id,
                error: item.error.map(|error| BatchItemError {
                    code: error.code,
                    message: error.message,
                    field: error.field,
                }),
            })
            .collect(),
    }
}

fn actor_request(actor: &ActorInput) -> models::EventActorRequest {
    models::EventActorRequest {
        r#type: actor.r#type.clone(),
        id: actor.id.clone(),
        name: actor.name.clone(),
        metadata: actor.metadata.clone(),
    }
}

fn target_request(target: &TargetInput) -> models::EventTargetRequest {
    models::EventTargetRequest {
        r#type: target.r#type.clone(),
        id: target.id.clone(),
        name: target.name.clone(),
        metadata: target.metadata.clone(),
    }
}

fn new_uuid() -> String {
    uuid::Uuid::new_v4().to_string()
}
