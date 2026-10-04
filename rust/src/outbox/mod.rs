//! The outbox behind [`Santati::log`](crate::Santati::log): the store
//! interface, the bounded in-memory store, and the worker's pass.

use std::any::Any;
use std::collections::{HashMap, VecDeque};
use std::fmt;
use std::panic::{catch_unwind, AssertUnwindSafe};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex};
use std::time::Duration;

use async_trait::async_trait;
use futures_util::FutureExt;
use tokio::sync::Notify;
use tokio::task::JoinHandle;

use crate::client::Santati;
use crate::error::Error;
use crate::types::{BatchStatus, EventInput};

#[cfg(feature = "redis")]
pub mod redis;

/// The default capacity of the built-in [`MemoryOutbox`].
pub(crate) const DEFAULT_MAX_PENDING: usize = 10_000;
const DEFAULT_BATCH_SIZE: usize = 100;
const DEFAULT_FLUSH_INTERVAL: Duration = Duration::from_millis(1_000);

type PreSend = Arc<dyn Fn(EventInput) -> Option<EventInput> + Send + Sync>;
type PostSend = Arc<dyn Fn(&EventInput, &SendOutcome) + Send + Sync>;

/// One stored event and the store's handle for it.
#[derive(Debug, Clone, PartialEq)]
pub struct OutboxEntry {
    /// The store's id for this entry (a stream id for Redis).
    pub id: String,
    /// The stored event: `trail` and `idempotency_key` are always set.
    pub event: EventInput,
}

/// How one event's delivery ended, as seen by `post_send`.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SendStatus {
    /// The event was indexed.
    Accepted,
    /// The event was already stored under the same idempotency key.
    Duplicate,
    /// The server rejected the event.
    Rejected,
    /// The event could not be sent (or a `pre_send` hook panicked).
    Failed,
}

impl SendStatus {
    /// The wire name of this status.
    pub fn as_str(self) -> &'static str {
        match self {
            SendStatus::Accepted => "accepted",
            SendStatus::Duplicate => "duplicate",
            SendStatus::Rejected => "rejected",
            SendStatus::Failed => "failed",
        }
    }
}

impl fmt::Display for SendStatus {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// What `post_send` receives next to the stored event.
#[derive(Debug, Clone, PartialEq)]
pub struct SendOutcome {
    /// How delivery ended.
    pub status: SendStatus,
    /// The stored event's id for an accepted or duplicate event.
    pub id: Option<String>,
    /// Why the event was rejected or could not be sent.
    pub error: Option<Error>,
}

/// Where [`Santati::log`] keeps events until the worker sends them.
///
/// A store must be safe to call from the worker and from `log` concurrently.
#[async_trait]
pub trait OutboxStore: Send + Sync {
    /// Store `event` at the tail; fail with [`Error::Outbox`] code
    /// `outbox_full` when the store is full.
    async fn enqueue(&self, event: EventInput) -> Result<(), Error>;

    /// Up to `limit` oldest entries, FIFO. A claimed entry is not returned by
    /// another claim until it is released (Redis: until it goes stale).
    /// `claim(0)` is empty.
    async fn claim(&self, limit: usize) -> Result<Vec<OutboxEntry>, Error>;

    /// Delete the entries permanently.
    async fn ack(&self, ids: Vec<String>) -> Result<(), Error>;

    /// Make the entries eligible for a later claim; timing is store-specific.
    async fn release(&self, ids: Vec<String>) -> Result<(), Error>;
}

#[derive(Debug)]
struct MemoryState {
    pending: VecDeque<OutboxEntry>,
    claimed: HashMap<String, OutboxEntry>,
    next_id: u64,
}

/// A bounded in-memory [`OutboxStore`]: the default.
#[derive(Debug)]
pub struct MemoryOutbox {
    max_pending: usize,
    state: Mutex<MemoryState>,
}

impl MemoryOutbox {
    /// A store holding at most `max_pending` entries, pending and claimed.
    ///
    /// Returns a [`Error::Validation`] (field `max_pending`) when
    /// `max_pending` is zero.
    pub fn new(max_pending: usize) -> Result<MemoryOutbox, Error> {
        if max_pending < 1 {
            return Err(Error::validation(
                "max_pending",
                "max_pending must be at least 1",
            ));
        }
        Ok(MemoryOutbox {
            max_pending,
            state: Mutex::new(MemoryState {
                pending: VecDeque::new(),
                claimed: HashMap::new(),
                next_id: 1,
            }),
        })
    }

    fn lock(&self) -> std::sync::MutexGuard<'_, MemoryState> {
        self.state
            .lock()
            .unwrap_or_else(|poisoned| poisoned.into_inner())
    }
}

#[async_trait]
impl OutboxStore for MemoryOutbox {
    async fn enqueue(&self, event: EventInput) -> Result<(), Error> {
        let mut state = self.lock();
        if state.pending.len() + state.claimed.len() >= self.max_pending {
            return Err(Error::outbox("outbox_full", "the outbox is full"));
        }
        let id = state.next_id.to_string();
        state.next_id += 1;
        state.pending.push_back(OutboxEntry { id, event });
        Ok(())
    }

    async fn claim(&self, limit: usize) -> Result<Vec<OutboxEntry>, Error> {
        let mut state = self.lock();
        let count = limit.min(state.pending.len());
        let entries: Vec<OutboxEntry> = state.pending.drain(..count).collect();
        for entry in &entries {
            state.claimed.insert(entry.id.clone(), entry.clone());
        }
        Ok(entries)
    }

    async fn ack(&self, ids: Vec<String>) -> Result<(), Error> {
        let mut state = self.lock();
        for id in &ids {
            state.claimed.remove(id);
        }
        Ok(())
    }

    async fn release(&self, ids: Vec<String>) -> Result<(), Error> {
        let mut state = self.lock();
        let mut entries: Vec<OutboxEntry> = ids
            .iter()
            .filter_map(|id| state.claimed.remove(id))
            .collect();
        entries.sort_by_key(|entry| entry.id.parse::<u64>().unwrap_or(u64::MAX));
        for entry in entries.into_iter().rev() {
            state.pending.push_front(entry);
        }
        Ok(())
    }
}

/// The builder's outbox options, before the client exists.
#[derive(Clone)]
pub(crate) struct OutboxConfig {
    pub(crate) store: Option<Arc<dyn OutboxStore>>,
    pub(crate) batch_size: usize,
    pub(crate) flush_interval: Duration,
    pub(crate) pre_send: Option<PreSend>,
    pub(crate) post_send: Option<PostSend>,
}

impl Default for OutboxConfig {
    fn default() -> OutboxConfig {
        OutboxConfig {
            store: None,
            batch_size: DEFAULT_BATCH_SIZE,
            flush_interval: DEFAULT_FLUSH_INTERVAL,
            pre_send: None,
            post_send: None,
        }
    }
}

impl fmt::Debug for OutboxConfig {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("OutboxConfig")
            .field("batch_size", &self.batch_size)
            .field("flush_interval", &self.flush_interval)
            .finish_non_exhaustive()
    }
}

/// The outbox state one client shares with all its clones.
pub(crate) struct OutboxState {
    store: Arc<dyn OutboxStore>,
    batch_size: usize,
    flush_interval: Duration,
    pre_send: Option<PreSend>,
    post_send: Option<PostSend>,
    pass_lock: tokio::sync::Mutex<()>,
    worker: Mutex<Option<JoinHandle<()>>>,
    notify: Notify,
    stopping: AtomicBool,
    closed: AtomicBool,
}

impl fmt::Debug for OutboxState {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("OutboxState")
            .field("batch_size", &self.batch_size)
            .field("flush_interval", &self.flush_interval)
            .field("closed", &self.closed.load(Ordering::SeqCst))
            .finish_non_exhaustive()
    }
}

impl OutboxState {
    pub(crate) fn new(
        store: Arc<dyn OutboxStore>,
        batch_size: usize,
        flush_interval: Duration,
        pre_send: Option<PreSend>,
        post_send: Option<PostSend>,
    ) -> Arc<OutboxState> {
        Arc::new(OutboxState {
            store,
            batch_size,
            flush_interval,
            pre_send,
            post_send,
            pass_lock: tokio::sync::Mutex::new(()),
            worker: Mutex::new(None),
            notify: Notify::new(),
            stopping: AtomicBool::new(false),
            closed: AtomicBool::new(false),
        })
    }

    /// Tell `post_send` about an event; a panic in the hook is ignored.
    fn post(&self, event: &EventInput, outcome: &SendOutcome) {
        if let Some(hook) = &self.post_send {
            let _ = catch_unwind(AssertUnwindSafe(|| hook(event, outcome)));
        }
    }

    fn failed(&self, event: &EventInput, error: Error) {
        self.post(
            event,
            &SendOutcome {
                status: SendStatus::Failed,
                id: None,
                error: Some(error),
            },
        );
    }

    /// Run `pre_send` on a copy of the stored event: `Err` is the panic text.
    fn pre(&self, event: &EventInput) -> Result<Option<EventInput>, String> {
        match &self.pre_send {
            None => Ok(Some(event.clone())),
            Some(hook) => catch_unwind(AssertUnwindSafe(|| hook(event.clone())))
                .map_err(|payload| panic_text(payload.as_ref())),
        }
    }
}

fn panic_text(payload: &(dyn Any + Send)) -> String {
    if let Some(text) = payload.downcast_ref::<&str>() {
        (*text).to_string()
    } else if let Some(text) = payload.downcast_ref::<String>() {
        text.clone()
    } else {
        "pre_send hook panicked".to_string()
    }
}

pub(crate) async fn log(client: &Santati, event: EventInput) -> Result<String, Error> {
    let state = client.outbox_state();
    if state.closed.load(Ordering::SeqCst) {
        return Err(Error::outbox("closed", "client is closed"));
    }
    let envelope = client.events().envelope(&event, "")?;
    let key = envelope.idempotency_key.clone().unwrap_or_default();
    let stored = EventInput {
        trail: Some(envelope.trail),
        idempotency_key: Some(key.clone()),
        data: event.data.filter(|data| !data.is_null()),
        ..event
    };
    state.store.enqueue(stored).await.map_err(store_error)?;
    start_worker(client);
    Ok(key)
}

pub(crate) async fn flush(client: &Santati) -> Result<(), Error> {
    let state = client.outbox_state();
    let _guard = state.pass_lock.lock().await;
    pass(client).await
}

pub(crate) async fn close(client: &Santati) -> Result<(), Error> {
    let state = client.outbox_state();
    if state.closed.swap(true, Ordering::SeqCst) {
        return Ok(());
    }
    state.stopping.store(true, Ordering::SeqCst);
    state.notify.notify_one();
    let worker = state
        .worker
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner())
        .take();
    if let Some(worker) = worker {
        let _ = worker.await;
    }
    flush(client).await
}

/// Stops the worker once every user-held client handle is gone; the worker's
/// own client copy carries no guard, so it cannot keep itself alive.
#[derive(Debug)]
pub(crate) struct WorkerGuard(pub(crate) Arc<OutboxState>);

impl Drop for WorkerGuard {
    fn drop(&mut self) {
        self.0.stopping.store(true, Ordering::SeqCst);
        self.0.notify.notify_one();
    }
}

/// Spawn the tick task on the first `log`; without a Tokio runtime there is
/// no worker and `flush`/`close` still send.
fn start_worker(client: &Santati) {
    let Ok(runtime) = tokio::runtime::Handle::try_current() else {
        return;
    };
    let state = client.outbox_state();
    let mut worker = state
        .worker
        .lock()
        .unwrap_or_else(|poisoned| poisoned.into_inner());
    if worker.is_some() || state.stopping.load(Ordering::SeqCst) {
        return;
    }
    let client = client.without_guard();
    *worker = Some(runtime.spawn(async move {
        let state = client.outbox_state().clone();
        loop {
            tokio::select! {
                _ = tokio::time::sleep(state.flush_interval) => {}
                _ = state.notify.notified() => {}
            }
            if state.stopping.load(Ordering::SeqCst) {
                break;
            }
            // Store failures and stray panics must not kill the worker.
            let tick = async {
                let _guard = state.pass_lock.lock().await;
                let _ = pass(&client).await;
            };
            let _ = AssertUnwindSafe(tick).catch_unwind().await;
        }
    }));
}

/// Only the store's own `OutboxError` passes through; anything else is a
/// store failure.
fn store_error(error: Error) -> Error {
    match error {
        Error::Outbox(_) => error,
        other => Error::outbox("store_unavailable", other.message().to_string()),
    }
}

fn is_retryable(error: &Error) -> bool {
    matches!(
        error,
        Error::Transport(_) | Error::Server(_) | Error::RateLimited(_)
    )
}

/// One pass: claim, hook, send and acknowledge until the outbox is drained or
/// a batch was released.
async fn pass(client: &Santati) -> Result<(), Error> {
    let state = client.outbox_state();
    loop {
        let entries = state
            .store
            .claim(state.batch_size)
            .await
            .map_err(store_error)?;
        if entries.is_empty() {
            return Ok(());
        }
        let mut released = false;
        let mut to_send: Vec<&OutboxEntry> = Vec::new();
        let mut outs: Vec<EventInput> = Vec::new();
        for entry in &entries {
            match state.pre(&entry.event) {
                Err(text) => {
                    state
                        .store
                        .release(vec![entry.id.clone()])
                        .await
                        .map_err(store_error)?;
                    released = true;
                    state.failed(&entry.event, Error::outbox("hook_failed", text));
                }
                Ok(None) => state
                    .store
                    .ack(vec![entry.id.clone()])
                    .await
                    .map_err(store_error)?,
                Ok(Some(out)) => {
                    to_send.push(entry);
                    outs.push(out);
                }
            }
        }
        if !to_send.is_empty() {
            let ids: Vec<String> = to_send.iter().map(|entry| entry.id.clone()).collect();
            match client.events().emit_batch_with_status(outs).await {
                Err(error) => {
                    for entry in &to_send {
                        state.failed(&entry.event, error.clone());
                    }
                    if is_retryable(&error) {
                        state.store.release(ids).await.map_err(store_error)?;
                        released = true;
                    } else {
                        state.store.ack(ids).await.map_err(store_error)?;
                    }
                }
                Ok((result, http_status)) => {
                    for item in result.results {
                        let Some(entry) = usize::try_from(item.index)
                            .ok()
                            .and_then(|index| to_send.get(index))
                        else {
                            continue;
                        };
                        let outcome = match item.status {
                            BatchStatus::Accepted | BatchStatus::Duplicate => SendOutcome {
                                status: if item.status == BatchStatus::Accepted {
                                    SendStatus::Accepted
                                } else {
                                    SendStatus::Duplicate
                                },
                                id: item.id,
                                error: None,
                            },
                            BatchStatus::Rejected => {
                                let rejection = item.error.unwrap_or_default();
                                SendOutcome {
                                    status: SendStatus::Rejected,
                                    id: None,
                                    error: Some(Error::Validation(crate::ErrorDetails {
                                        status: Some(http_status),
                                        code: Some(rejection.code),
                                        field: rejection.field,
                                        retry_after: None,
                                        message: rejection.message,
                                    })),
                                }
                            }
                        };
                        state.post(&entry.event, &outcome);
                    }
                    state.store.ack(ids).await.map_err(store_error)?;
                }
            }
        }
        if released || entries.len() < state.batch_size {
            return Ok(());
        }
    }
}
