//! The client and its builder: transport, options and shared request plumbing.

use std::sync::Arc;
use std::time::Duration;

use reqwest::header::{HeaderMap, HeaderName, HeaderValue, AUTHORIZATION};
use reqwest::redirect::Policy;
use reqwest::Client as HttpClient;

use crate::error::Error;
use crate::events::Events;
use crate::outbox::{self, OutboxConfig, OutboxState, OutboxStore, SendOutcome, WorkerGuard};
use crate::retry::RetryPolicy;
use crate::schemas::Schemas;
use crate::types::EventInput;

/// The API origin used when `base_url` is not set.
pub const DEFAULT_BASE_URL: &str = "https://api.santati.io";
/// Per-attempt timeout used when `timeout_ms` is not set.
pub const DEFAULT_TIMEOUT_MS: u64 = 10_000;
/// Retries used when `max_retries` is not set.
pub const DEFAULT_MAX_RETRIES: u32 = 2;
/// Backoff base used when `initial_backoff_ms` is not set.
pub const DEFAULT_INITIAL_BACKOFF_MS: u64 = 250;
/// Backoff cap used when `max_backoff_ms` is not set.
pub const DEFAULT_MAX_BACKOFF_MS: u64 = 8_000;

const USER_AGENT: &str = concat!("santati-rust/", env!("CARGO_PKG_VERSION"));

/// A Santati client: one API key, one origin, one HTTP connection pool.
///
/// Build one with [`Santati::builder`] and reach the API through
/// [`Santati::events`].
#[derive(Debug, Clone)]
pub struct Santati {
    http: HttpClient,
    base_url: String,
    trail: Option<String>,
    timeout: Duration,
    max_retries: u32,
    initial_backoff: Duration,
    max_backoff: Duration,
    outbox: Option<Arc<OutboxState>>,
    _guard: Option<Arc<WorkerGuard>>,
}

impl Santati {
    /// Start building a client for `api_key`.
    pub fn builder(api_key: impl Into<String>) -> Builder {
        Builder::new(api_key)
    }

    /// The event operations.
    pub fn events(&self) -> Events<'_> {
        Events::new(self)
    }

    /// The schema operations: event definitions, schema versions and the
    /// standard packs.
    pub fn schemas(&self) -> Schemas<'_> {
        Schemas::new(self)
    }

    /// Run one outbox pass now: claim, send and acknowledge batches until the
    /// outbox is empty or a batch is released for a later pass.
    ///
    /// Send failures never fail the call; they are reported through
    /// `post_send`. Returns [`Error::Outbox`] (`store_unavailable`) when the
    /// store fails. Without an outbox it returns `Ok(())` at once.
    pub async fn flush(&self) -> Result<(), Error> {
        match self.outbox_state() {
            Some(state) => outbox::flush(self, state).await,
            None => Ok(()),
        }
    }

    /// Stop the worker, wait for a pass in progress, then [`Santati::flush`].
    /// Idempotent: later calls return `Ok(())` at once. After `close`, a
    /// queued [`Events::emit`] fails with [`Error::Outbox`] code `closed`.
    /// Without an outbox, `flush` and `close` return `Ok(())` at once.
    pub async fn close(&self) -> Result<(), Error> {
        match self.outbox_state() {
            Some(state) => outbox::close(self, state).await,
            None => Ok(()),
        }
    }

    /// A copy for the worker task: it must not count as a user handle.
    pub(crate) fn without_guard(&self) -> Santati {
        Santati {
            _guard: None,
            ..self.clone()
        }
    }

    pub(crate) fn outbox_state(&self) -> Option<&Arc<OutboxState>> {
        self.outbox.as_ref()
    }

    /// The origin every request goes to, trailing `/` removed.
    pub fn base_url(&self) -> &str {
        &self.base_url
    }

    /// The default trail for emits.
    pub fn trail(&self) -> Option<&str> {
        self.trail.as_deref()
    }

    /// The per-attempt timeout.
    pub fn timeout(&self) -> Duration {
        self.timeout
    }

    /// The number of retries after the first attempt.
    pub fn max_retries(&self) -> u32 {
        self.max_retries
    }

    /// The full URL of an API path.
    pub(crate) fn endpoint(&self, path: &str) -> String {
        format!("{}{path}", self.base_url)
    }

    /// The shared HTTP client: no redirects, no library retries, and the
    /// `Authorization` header on every request.
    pub(crate) fn http(&self) -> &HttpClient {
        &self.http
    }

    pub(crate) fn retry_policy(&self) -> RetryPolicy {
        RetryPolicy {
            max_retries: self.max_retries,
            initial_backoff: self.initial_backoff,
            max_backoff: self.max_backoff,
        }
    }

    /// The trail an event falls back to, ignoring an empty one.
    pub(crate) fn default_trail(&self) -> Option<&str> {
        self.trail.as_deref().filter(|trail| !trail.is_empty())
    }
}

/// Builds a [`Santati`]; every option has a documented default and
/// [`Builder::build`] validates the two that can be wrong.
#[derive(Debug, Clone)]
pub struct Builder {
    api_key: String,
    base_url: String,
    trail: Option<String>,
    timeout: Duration,
    max_retries: u32,
    initial_backoff: Duration,
    max_backoff: Duration,
    headers: Vec<(String, String)>,
    outbox: OutboxConfig,
}

impl Builder {
    fn new(api_key: impl Into<String>) -> Builder {
        Builder {
            api_key: api_key.into(),
            base_url: DEFAULT_BASE_URL.to_string(),
            trail: None,
            timeout: Duration::from_millis(DEFAULT_TIMEOUT_MS),
            max_retries: DEFAULT_MAX_RETRIES,
            initial_backoff: Duration::from_millis(DEFAULT_INITIAL_BACKOFF_MS),
            max_backoff: Duration::from_millis(DEFAULT_MAX_BACKOFF_MS),
            headers: Vec::new(),
            outbox: OutboxConfig::default(),
        }
    }

    /// The API origin; a trailing `/` is removed and a path prefix is kept.
    pub fn base_url(mut self, base_url: impl Into<String>) -> Builder {
        self.base_url = base_url.into();
        self
    }

    /// The trail emits fall back to when the event does not name one.
    pub fn trail(mut self, trail: impl Into<String>) -> Builder {
        self.trail = Some(trail.into());
        self
    }

    /// The per-attempt timeout.
    pub fn timeout(mut self, timeout: Duration) -> Builder {
        self.timeout = timeout;
        self
    }

    /// How many times to retry a retryable failure.
    pub fn max_retries(mut self, max_retries: u32) -> Builder {
        self.max_retries = max_retries;
        self
    }

    /// The exponential backoff's base and cap.
    pub fn backoff(mut self, initial: Duration, max: Duration) -> Builder {
        self.initial_backoff = initial;
        self.max_backoff = max;
        self
    }

    /// An extra header sent on every request.
    pub fn header(mut self, name: impl Into<String>, value: impl Into<String>) -> Builder {
        self.headers.push((name.into(), value.into()));
        self
    }

    /// Makes [`Events::emit`] store events in `store` for the background worker
    /// instead of sending them; there is no outbox by default.
    pub fn outbox(mut self, store: impl OutboxStore + 'static) -> Builder {
        self.outbox.store = Some(Arc::new(store));
        self
    }

    /// Envelopes per outbox request, `1..=500`; default 100.
    pub fn batch_size(mut self, batch_size: usize) -> Builder {
        self.outbox.batch_size = batch_size;
        self
    }

    /// The outbox worker's tick, greater than zero; default one second.
    pub fn flush_interval(mut self, interval: Duration) -> Builder {
        self.outbox.flush_interval = interval;
        self
    }

    /// A hook called on each stored event right before its batch request:
    /// return the event (possibly modified) to send it, `None` to drop it. A
    /// panic leaves the event in the outbox and reports a `failed` outcome
    /// with an `hook_failed` [`Error::Outbox`].
    pub fn pre_send(
        mut self,
        hook: impl Fn(EventInput) -> Option<EventInput> + Send + Sync + 'static,
    ) -> Builder {
        self.outbox.pre_send = Some(Arc::new(hook));
        self
    }

    /// A hook called with each event's stored form and its [`SendOutcome`].
    /// Panics in it are caught and ignored.
    pub fn post_send(
        mut self,
        hook: impl Fn(&EventInput, &SendOutcome) + Send + Sync + 'static,
    ) -> Builder {
        self.outbox.post_send = Some(Arc::new(hook));
        self
    }

    /// Validate the options and build the client.
    ///
    /// Returns a [`Error::Validation`] (status `None`) for an empty `api_key`
    /// (field `api_key`), a header named `Authorization` (field `headers`), a
    /// `batch_size` outside `1..=500` (field `batch_size`) or a zero
    /// `flush_interval` (field `flush_interval_ms`).
    pub fn build(self) -> Result<Santati, Error> {
        if self.api_key.is_empty() {
            return Err(Error::validation("api_key", "api_key must not be empty"));
        }
        if !(1..=500).contains(&self.outbox.batch_size) {
            return Err(Error::validation(
                "batch_size",
                "batch_size must be between 1 and 500",
            ));
        }
        if self.outbox.flush_interval.is_zero() {
            return Err(Error::validation(
                "flush_interval_ms",
                "flush_interval_ms must be greater than 0",
            ));
        }

        let mut headers = HeaderMap::new();
        for (name, value) in &self.headers {
            if name.eq_ignore_ascii_case("authorization") {
                return Err(Error::validation(
                    "headers",
                    "the authorization header comes from api_key and cannot be overridden",
                ));
            }
            let header_name = HeaderName::try_from(name.as_str()).map_err(|_| {
                Error::validation("headers", format!("invalid header name: {name}"))
            })?;
            let header_value = HeaderValue::try_from(value.as_str()).map_err(|_| {
                Error::validation("headers", format!("invalid value for header {name}"))
            })?;
            headers.insert(header_name, header_value);
        }
        let authorization = HeaderValue::try_from(format!("Api-Key {}", self.api_key))
            .map_err(|_| Error::validation("api_key", "api_key is not a valid header value"))?;
        headers.insert(AUTHORIZATION, authorization);

        let http = HttpClient::builder()
            .redirect(Policy::none())
            .retry(reqwest::retry::never())
            .timeout(self.timeout)
            .user_agent(USER_AGENT)
            .default_headers(headers)
            .build()
            .map_err(|error| {
                Error::transport(format!("could not build the HTTP client: {error}"))
            })?;

        let outbox = self.outbox.store.map(|store| {
            OutboxState::new(
                store,
                self.outbox.batch_size,
                self.outbox.flush_interval,
                self.outbox.pre_send,
                self.outbox.post_send,
            )
        });

        Ok(Santati {
            http,
            base_url: self.base_url.trim_end_matches('/').to_string(),
            trail: self.trail,
            timeout: self.timeout,
            max_retries: self.max_retries,
            initial_backoff: self.initial_backoff,
            max_backoff: self.max_backoff,
            _guard: outbox.clone().map(|state| Arc::new(WorkerGuard(state))),
            outbox,
        })
    }
}
