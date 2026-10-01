"""Retry policy: which failures are retried, and how long to wait first.

The facade is the only retry loop (the generated core is built with
``Retry(total=0, redirect=False)``), so every attempt re-sends the identical
request: same body, same idempotency keys.
"""

from __future__ import annotations

import random
import time
from collections.abc import Callable
from typing import TypeVar

from ._errors import RateLimitedError, SantatiError, ServerError, TransportError

T = TypeVar("T")

RETRYABLE_SERVER_STATUSES = frozenset({500, 502, 503, 504})


class RetryPolicy:
    """The client's attempt budget, backoff and ``Retry-After`` handling."""

    def __init__(self, max_retries: int, initial_backoff_ms: int, max_backoff_ms: int) -> None:
        self.max_retries = max_retries
        self.initial_backoff_ms = initial_backoff_ms
        self.max_backoff_ms = max_backoff_ms

    def is_retryable(self, error: SantatiError) -> bool:
        """Whether ``error`` should be retried at all (budget aside)."""
        if isinstance(error, TransportError):
            return True
        if isinstance(error, ServerError):
            return error.status in RETRYABLE_SERVER_STATUSES
        if isinstance(error, RateLimitedError):
            return error.code != "quota_exceeded"
        return False

    def wait_before_retry(self, error: SantatiError, attempt: int) -> bool:
        """Sleep before the retry that follows failed ``attempt`` (1-based).

        Returns ``False`` when the server asked for longer than
        ``max_backoff_ms``: the caller must raise instead of retrying.
        """
        if error.retry_after is not None:
            delay_ms = error.retry_after * 1000
            if delay_ms > self.max_backoff_ms:
                return False
        else:
            ceiling = min(self.initial_backoff_ms * 2 ** (attempt - 1), self.max_backoff_ms)
            delay_ms = random.randint(0, ceiling)
        time.sleep(delay_ms / 1000)
        return True

    def run(self, operation: Callable[[], T]) -> T:
        """Run ``operation``, retrying a retryable failure up to ``max_retries`` times."""
        attempt = 0
        while True:
            attempt += 1
            try:
                return operation()
            except SantatiError as error:
                if attempt > self.max_retries or not self.is_retryable(error):
                    raise
                if not self.wait_before_retry(error, attempt):
                    raise
