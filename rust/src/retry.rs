//! The facade's retry loop: identical requests, exponential backoff, and the
//! server's `Retry-After` when it sends one.

use std::future::Future;
use std::time::Duration;

use crate::error::Error;

/// How many times to retry a request, and how long to wait between attempts.
#[derive(Debug, Clone, Copy)]
pub(crate) struct RetryPolicy {
    pub(crate) max_retries: u32,
    pub(crate) initial_backoff: Duration,
    pub(crate) max_backoff: Duration,
}

impl RetryPolicy {
    /// Transport failures, 500/502/503/504 and 429 are retryable; a 429 whose
    /// code is `quota_exceeded` is not.
    fn retryable(&self, error: &Error) -> bool {
        match error {
            Error::Transport(_) => true,
            Error::Server(details) => matches!(details.status, Some(500 | 502 | 503 | 504)),
            Error::RateLimited(details) => details.code.as_deref() != Some("quota_exceeded"),
            _ => false,
        }
    }

    /// How long to wait before retry `attempt` (1-based), or `None` when the
    /// server asked for longer than `max_backoff` and the error stands.
    fn delay(&self, attempt: u32, error: &Error) -> Option<Duration> {
        if let Some(seconds) = error.retry_after() {
            let millis = seconds.saturating_mul(1000);
            if millis > self.max_backoff.as_millis() as u64 {
                return None;
            }
            return Some(Duration::from_millis(millis));
        }

        let ceiling = (self.initial_backoff.as_millis() as u64)
            .saturating_mul(2u64.saturating_pow(attempt - 1))
            .min(self.max_backoff.as_millis() as u64);
        Some(Duration::from_millis(fastrand::u64(0..=ceiling)))
    }
}

/// Run `attempt` until it succeeds, its error is not retryable, or the retry
/// budget is spent. `attempt` is re-invoked with the identical request.
pub(crate) async fn run<F, Fut, T>(policy: RetryPolicy, mut attempt: F) -> Result<T, Error>
where
    F: FnMut() -> Fut,
    Fut: Future<Output = Result<T, Error>>,
{
    let mut retries = 0u32;
    loop {
        let error = match attempt().await {
            Ok(value) => return Ok(value),
            Err(error) => error,
        };
        if retries >= policy.max_retries || !policy.retryable(&error) {
            return Err(error);
        }
        let delay = match policy.delay(retries + 1, &error) {
            Some(delay) => delay,
            None => return Err(error),
        };
        retries += 1;
        if !delay.is_zero() {
            tokio::time::sleep(delay).await;
        }
    }
}
