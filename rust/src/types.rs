//! The facade's input and result types.
//!
//! `AuditEvent`, `EventActor` and `EventTarget` are the generated read models,
//! re-exported from the crate root; everything here is hand-written.

use std::collections::HashMap;

/// Who acted, as supplied to [`crate::Events::emit`].
///
/// Only `type` is required; the server's ingest is the validator.
#[derive(Debug, Clone, Default)]
pub struct ActorInput {
    /// Actor kind: `user`, `system` or `anonymous`.
    pub r#type: String,
    /// The actor's id in the producer's own namespace.
    pub id: Option<String>,
    /// Human-readable actor name.
    pub name: Option<String>,
    /// Actor metadata.
    pub metadata: Option<HashMap<String, String>>,
}

/// One object an event acted on, as supplied to [`crate::Events::emit`].
#[derive(Debug, Clone, Default)]
pub struct TargetInput {
    /// Target kind, e.g. `invoice`.
    pub r#type: String,
    /// The target's id in the producer's own namespace.
    pub id: String,
    /// Human-readable target name.
    pub name: Option<String>,
    /// Target metadata.
    pub metadata: Option<HashMap<String, String>>,
}

/// One event to emit, in the envelope's own shape.
///
/// Absent members are omitted from the request; `null` is never sent. `trail`
/// falls back to the client's default trail, and `idempotency_key` to a freshly
/// generated lowercase UUIDv4.
#[derive(Debug, Clone, Default)]
pub struct EventInput {
    /// The event type, e.g. `invoice.voided`.
    pub event: String,
    /// The audit log trail to index this event on.
    pub trail: Option<String>,
    /// The end-customer organization this event belongs to.
    pub organization_id: Option<String>,
    /// RFC 3339 with an offset, forwarded verbatim.
    pub created_at: Option<String>,
    /// Replay guard; generated when absent.
    pub idempotency_key: Option<String>,
    /// Who acted.
    pub actor: Option<ActorInput>,
    /// The objects the event acted on; at most 10.
    pub targets: Option<Vec<TargetInput>>,
    /// Event metadata.
    pub metadata: Option<HashMap<String, String>>,
    /// The producer's own payload, stored verbatim.
    pub data: Option<serde_json::Value>,
    /// Extra producer context, stored verbatim.
    pub context: Option<serde_json::Map<String, serde_json::Value>>,
}

impl EventInput {
    /// An event with only the event type set.
    pub fn new(event: impl Into<String>) -> Self {
        Self {
            event: event.into(),
            ..Default::default()
        }
    }
}

/// The filters [`crate::Events::list`] and [`crate::Events::iterate`] accept.
///
/// Every member is optional and absent ones are not sent. `limit` and `cursor`
/// apply to `list`; `iterate` manages the cursor itself.
#[derive(Debug, Clone, Default)]
pub struct ListParams {
    /// Only events on this trail.
    pub trail: Option<String>,
    /// Only this exact event type.
    pub event: Option<String>,
    /// Only event types with this prefix.
    pub event_prefix: Option<String>,
    /// Only events attributed to this organization.
    pub organization_id: Option<String>,
    /// Only events whose actor has this id.
    pub actor_id: Option<String>,
    /// Only events whose actor has this type.
    pub actor_type: Option<String>,
    /// Only events with a target of this type.
    pub target_type: Option<String>,
    /// Only events with a target of this id.
    pub target_id: Option<String>,
    /// Only events created at or after this instant.
    pub created_after: Option<String>,
    /// Only events created at or before this instant.
    pub created_before: Option<String>,
    /// Full-text query.
    pub q: Option<String>,
    /// Result ordering; `relevance` requires `q`.
    pub sort: Option<String>,
    /// Page size.
    pub limit: Option<u32>,
    /// Opaque cursor from a previous page.
    pub cursor: Option<String>,
}

/// The result of [`crate::Events::emit`].
#[derive(Debug, Clone, PartialEq)]
pub struct EmitResult {
    /// The stored event.
    pub event: crate::AuditEvent,
    /// True when the server replayed an earlier request with the same key.
    pub duplicate: bool,
    /// The idempotency key the request carried.
    pub idempotency_key: String,
}

/// Why one batch item was rejected.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct BatchItemError {
    /// The server's reserved failure code.
    pub code: String,
    /// Human-readable explanation.
    pub message: String,
    /// The request field at fault, when one is.
    pub field: Option<String>,
}

/// How one batch item ended up.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum BatchStatus {
    /// The event was indexed.
    Accepted,
    /// The event was already stored under the same idempotency key.
    Duplicate,
    /// The event was rejected.
    Rejected,
}

impl BatchStatus {
    /// The wire name of this status.
    pub fn as_str(self) -> &'static str {
        match self {
            BatchStatus::Accepted => "accepted",
            BatchStatus::Duplicate => "duplicate",
            BatchStatus::Rejected => "rejected",
        }
    }
}

impl std::fmt::Display for BatchStatus {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.as_str())
    }
}

/// One entry of [`BatchResult::results`], in submission order.
#[derive(Debug, Clone, PartialEq)]
pub struct BatchItem {
    /// The position of this item in the submitted batch.
    pub index: i32,
    /// What happened to this item.
    pub status: BatchStatus,
    /// The stored event's id for an accepted or duplicate item.
    pub id: Option<String>,
    /// The rejection for a rejected item.
    pub error: Option<BatchItemError>,
}

/// The result of [`crate::Events::emit_batch`].
#[derive(Debug, Clone, PartialEq)]
pub struct BatchResult {
    /// How many events this request indexed.
    pub accepted: i32,
    /// How many events this request rejected.
    pub rejected: i32,
    /// One entry per submitted event, in submission order.
    pub results: Vec<BatchItem>,
}

/// One page of [`crate::Events::list`].
#[derive(Debug, Clone, PartialEq)]
pub struct EventPage {
    /// The events on this page.
    pub results: Vec<crate::AuditEvent>,
    /// The cursor to pass to the next `list` call, or `None` on the last page.
    pub next_cursor: Option<String>,
}
