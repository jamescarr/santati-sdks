//! Official Rust SDK for the Santati audit-log API.
//!
//! Emit one audit event or a batch, list events with filters and cursor
//! pagination, and walk every page with [`Events::iterate`]. The crate is
//! async and needs a Tokio runtime.
//!
//! ```no_run
//! use santati::{EventInput, ListParams, Santati, StreamExt};
//!
//! # async fn run() -> Result<(), santati::Error> {
//! let client = Santati::builder("sat_sk_...").trail("billing").build()?;
//! let events = client.events();
//!
//! let result = events
//!     .emit(EventInput {
//!         event: "invoice.voided".into(),
//!         organization_id: Some("org_acme".into()),
//!         ..Default::default()
//!     })
//!     .await?;
//! if let Some(event) = &result.event {
//!     println!("{} (duplicate: {})", event.id, result.duplicate);
//! }
//!
//! let mut stream = std::pin::pin!(events.iterate(ListParams {
//!     trail: Some("billing".into()),
//!     ..Default::default()
//! }));
//! while let Some(event) = stream.next().await {
//!     println!("{}", event?.event);
//! }
//! # Ok(())
//! # }
//! ```
//!
//! # Outbox
//!
//! With [`Builder::outbox`], [`Events::emit`] validates like a plain `emit`,
//! stores the envelope in the [`OutboxStore`] and returns at once with
//! [`EmitResult::queued`] set and its idempotency key, without making a
//! request. A background task drains the outbox in batches; [`Santati::flush`]
//! and [`Santati::close`] drain it synchronously, so call `close` before the
//! program exits.
//!
//! ```no_run
//! use santati::{EventInput, MemoryOutbox, Santati};
//!
//! # async fn run() -> Result<(), santati::Error> {
//! let client = Santati::builder("sat_sk_...")
//!     .trail("billing")
//!     .outbox(MemoryOutbox::new(10_000)?)
//!     .build()?;
//! let result = client.events().emit(EventInput::new("invoice.voided")).await?;
//! assert!(result.queued);
//! client.close().await?;
//! # Ok(())
//! # }
//! ```
//!
//! The surface this implements, and its error kinds and retry policy, are
//! documented in `docs/sdk-surface.md`. Event definitions, their JSON Schema
//! versions and the standard packs are managed through [`Santati::schemas`].

mod client;
mod error;
mod events;
mod http;
mod outbox;
mod retry;
mod schemas;
mod types;

#[path = "generated/models/mod.rs"]
#[rustfmt::skip]
#[allow(dead_code, unused_imports, unused_variables, clippy::all)]
mod models;

pub use async_trait::async_trait;
pub use client::{
    Builder, Santati, DEFAULT_BASE_URL, DEFAULT_INITIAL_BACKOFF_MS, DEFAULT_MAX_BACKOFF_MS,
    DEFAULT_MAX_RETRIES, DEFAULT_TIMEOUT_MS,
};
pub use error::{Error, ErrorDetails, ErrorKind};
pub use events::Events;
pub use futures_util::StreamExt;
pub use models::{
    AuditEvent, EventActor, EventDefinition, EventSchemaVersion, EventTarget, OcsfMapping,
    SchemaCheck, SchemaCheckFailure, StandardEvent, StandardEventCatalog, StandardPack,
    StandardPackInstallResult,
};
#[cfg(feature = "redis")]
pub use outbox::redis::RedisOutbox;
pub use outbox::{MemoryOutbox, OutboxEntry, OutboxStore, SendOutcome, SendStatus};
pub use schemas::Schemas;
pub use types::{
    ActorInput, BatchItem, BatchItemError, BatchResult, BatchStatus, DefinitionInput,
    DefinitionPage, DefinitionUpdate, EmitResult, EventInput, EventPage, ListParams, PageParams,
    SchemaVersionPage, SchemaVersionResult, TargetInput,
};
