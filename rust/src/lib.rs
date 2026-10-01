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
//! println!("{} (duplicate: {})", result.event.id, result.duplicate);
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
//! The surface this implements, and its error kinds and retry policy, are
//! documented in `docs/sdk-surface.md`.

mod client;
mod error;
mod events;
mod retry;
mod types;

#[path = "generated/models/mod.rs"]
#[rustfmt::skip]
#[allow(dead_code, unused_imports, unused_variables, clippy::all)]
mod models;

pub use client::{
    Builder, Santati, DEFAULT_BASE_URL, DEFAULT_INITIAL_BACKOFF_MS, DEFAULT_MAX_BACKOFF_MS,
    DEFAULT_MAX_RETRIES, DEFAULT_TIMEOUT_MS,
};
pub use error::{Error, ErrorDetails, ErrorKind};
pub use events::Events;
pub use futures_util::StreamExt;
pub use models::{AuditEvent, EventActor, EventTarget};
pub use types::{
    ActorInput, BatchItem, BatchItemError, BatchResult, BatchStatus, EmitResult, EventInput,
    EventPage, ListParams, TargetInput,
};
