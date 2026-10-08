//! The SDK's error type: one enum with nine kinds, each carrying the same
//! details.

use std::fmt;

use reqwest::header::HeaderMap;

/// Which of the nine failure kinds an [`Error`] is.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum ErrorKind {
    /// Local validation, or an HTTP 400, 413 or 422 (any code but `schema_validation_failed`).
    Validation,
    /// HTTP 400, 413 or 422 whose code is `schema_validation_failed`: the event
    /// broke the action's JSON Schema, named a disallowed target type, or
    /// pinned an unusable `schema_version`.
    SchemaValidation,
    /// HTTP 401 or 403.
    Auth,
    /// HTTP 404.
    NotFound,
    /// HTTP 429.
    RateLimited,
    /// HTTP 500-599.
    Server,
    /// No HTTP response at all: refused, DNS, TLS or timeout.
    Transport,
    /// Any other non-2xx, an unexpected 2xx, or an undecodable 2xx body.
    Api,
    /// The outbox store refused or failed, or a `pre_send` hook raised.
    Outbox,
}

impl ErrorKind {
    /// The kind's name, e.g. `"RateLimitedError"`.
    pub fn as_str(self) -> &'static str {
        match self {
            ErrorKind::Validation => "ValidationError",
            ErrorKind::SchemaValidation => "SchemaValidationError",
            ErrorKind::Auth => "AuthError",
            ErrorKind::NotFound => "NotFoundError",
            ErrorKind::RateLimited => "RateLimitedError",
            ErrorKind::Server => "ServerError",
            ErrorKind::Transport => "TransportError",
            ErrorKind::Api => "ApiError",
            ErrorKind::Outbox => "OutboxError",
        }
    }
}

impl fmt::Display for ErrorKind {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(self.as_str())
    }
}

/// Everything known about a failure.
#[derive(Debug, Clone, Default, PartialEq, Eq)]
pub struct ErrorDetails {
    /// The HTTP status, when a response was received.
    pub status: Option<u16>,
    /// The server's reserved error code.
    pub code: Option<String>,
    /// The request field at fault.
    pub field: Option<String>,
    /// The `Retry-After` delay in seconds, when the response carried one.
    pub retry_after: Option<u64>,
    /// A human-readable message; never the thing to branch on.
    pub message: String,
}

/// Every failure the SDK reports, and its [`ErrorDetails`].
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    /// Local validation, or an HTTP 400, 413 or 422 (any code but `schema_validation_failed`).
    Validation(ErrorDetails),
    /// HTTP 400, 413 or 422 whose code is `schema_validation_failed`: the event
    /// broke the action's JSON Schema, named a disallowed target type, or
    /// pinned an unusable `schema_version`.
    SchemaValidation(ErrorDetails),
    /// HTTP 401 or 403.
    Auth(ErrorDetails),
    /// HTTP 404.
    NotFound(ErrorDetails),
    /// HTTP 429.
    RateLimited(ErrorDetails),
    /// HTTP 500-599.
    Server(ErrorDetails),
    /// No HTTP response at all.
    Transport(ErrorDetails),
    /// Any other non-2xx, an unexpected 2xx, or an undecodable 2xx body.
    Api(ErrorDetails),
    /// The outbox store refused or failed, or a `pre_send` hook raised.
    Outbox(ErrorDetails),
}

impl Error {
    /// This error's details.
    pub fn details(&self) -> &ErrorDetails {
        match self {
            Error::Validation(details)
            | Error::SchemaValidation(details)
            | Error::Auth(details)
            | Error::NotFound(details)
            | Error::RateLimited(details)
            | Error::Server(details)
            | Error::Transport(details)
            | Error::Api(details)
            | Error::Outbox(details) => details,
        }
    }

    /// Which of the nine kinds this error is.
    pub fn kind(&self) -> ErrorKind {
        match self {
            Error::Validation(_) => ErrorKind::Validation,
            Error::SchemaValidation(_) => ErrorKind::SchemaValidation,
            Error::Auth(_) => ErrorKind::Auth,
            Error::NotFound(_) => ErrorKind::NotFound,
            Error::RateLimited(_) => ErrorKind::RateLimited,
            Error::Server(_) => ErrorKind::Server,
            Error::Transport(_) => ErrorKind::Transport,
            Error::Api(_) => ErrorKind::Api,
            Error::Outbox(_) => ErrorKind::Outbox,
        }
    }

    /// The HTTP status, when a response was received.
    pub fn status(&self) -> Option<u16> {
        self.details().status
    }

    /// The server's reserved error code.
    pub fn code(&self) -> Option<&str> {
        self.details().code.as_deref()
    }

    /// The request field at fault.
    pub fn field(&self) -> Option<&str> {
        self.details().field.as_deref()
    }

    /// The `Retry-After` delay in seconds, when the response carried one.
    pub fn retry_after(&self) -> Option<u64> {
        self.details().retry_after
    }

    /// A human-readable message.
    pub fn message(&self) -> &str {
        &self.details().message
    }

    pub(crate) fn from_parts(kind: ErrorKind, details: ErrorDetails) -> Error {
        match kind {
            ErrorKind::Validation => Error::Validation(details),
            ErrorKind::SchemaValidation => Error::SchemaValidation(details),
            ErrorKind::Auth => Error::Auth(details),
            ErrorKind::NotFound => Error::NotFound(details),
            ErrorKind::RateLimited => Error::RateLimited(details),
            ErrorKind::Server => Error::Server(details),
            ErrorKind::Transport => Error::Transport(details),
            ErrorKind::Api => Error::Api(details),
            ErrorKind::Outbox => Error::Outbox(details),
        }
    }

    /// A local validation failure: no request was made.
    pub(crate) fn validation(field: impl Into<String>, message: impl Into<String>) -> Error {
        Error::Validation(ErrorDetails {
            field: Some(field.into()),
            message: message.into(),
            ..Default::default()
        })
    }

    /// An outbox failure: the store refused or failed, or a hook raised.
    pub(crate) fn outbox(code: &str, message: impl Into<String>) -> Error {
        Error::Outbox(ErrorDetails {
            code: Some(code.to_string()),
            message: message.into(),
            ..Default::default()
        })
    }

    /// A failure with no HTTP response behind it.
    pub(crate) fn transport(message: impl Into<String>) -> Error {
        Error::Transport(ErrorDetails {
            message: message.into(),
            ..Default::default()
        })
    }

    /// An unexpected or undecodable 2xx.
    pub(crate) fn api(status: u16) -> Error {
        Error::Api(ErrorDetails {
            status: Some(status),
            message: format!("HTTP {status}"),
            ..Default::default()
        })
    }

    /// Map a non-2xx response: kind from the status, details from the body.
    pub(crate) fn from_http(status: u16, headers: &HeaderMap, body: &[u8]) -> Error {
        let mut details = ErrorDetails {
            status: Some(status),
            message: format!("HTTP {status}"),
            ..Default::default()
        };

        if let Some(value) = headers
            .get(reqwest::header::RETRY_AFTER)
            .and_then(|value| value.to_str().ok())
            .filter(|value| !value.is_empty() && value.bytes().all(|b| b.is_ascii_digit()))
        {
            details.retry_after = value.parse().ok();
        }

        if let Ok(serde_json::Value::Object(body)) =
            serde_json::from_slice::<serde_json::Value>(body)
        {
            let envelope = body
                .get("error")
                .and_then(serde_json::Value::as_object)
                .and_then(|error| {
                    error
                        .get("code")
                        .and_then(serde_json::Value::as_str)
                        .map(|code| (error, code))
                });
            match envelope {
                Some((error, code)) => {
                    details.code = Some(code.to_string());
                    details.field = error
                        .get("field")
                        .and_then(serde_json::Value::as_str)
                        .map(str::to_string);
                    if let Some(message) = error.get("message").and_then(serde_json::Value::as_str)
                    {
                        details.message = message.to_string();
                    }
                }
                None => {
                    if let Some(detail) = body.get("detail").and_then(serde_json::Value::as_str) {
                        details.message = detail.to_string();
                    }
                }
            }
        }

        let kind = match status {
            400 | 413 | 422 => validation_kind(details.code.as_deref()),
            401 | 403 => ErrorKind::Auth,
            404 => ErrorKind::NotFound,
            429 => ErrorKind::RateLimited,
            500..=599 => ErrorKind::Server,
            _ => ErrorKind::Api,
        };
        Error::from_parts(kind, details)
    }
}

/// The kind of a 400, 413 or 422 whose body carried `code`.
pub(crate) fn validation_kind(code: Option<&str>) -> ErrorKind {
    if code == Some("schema_validation_failed") {
        ErrorKind::SchemaValidation
    } else {
        ErrorKind::Validation
    }
}

impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        write!(f, "{}: {}", self.kind(), self.message())
    }
}

impl std::error::Error for Error {}
