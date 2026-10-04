//! The Redis Streams [`OutboxStore`], behind the `redis` feature.
//!
//! The layout is shared by every Santati SDK: one stream, one consumer group
//! `santati`, one `event` field holding the compact wire JSON.

use std::time::Duration;

use async_trait::async_trait;
use redis::aio::ConnectionManager;
use redis::Value;
use tokio::sync::OnceCell;

use super::{OutboxEntry, OutboxStore};
use crate::error::Error;
use crate::types::EventInput;

const GROUP: &str = "santati";

/// An [`OutboxStore`] over Redis Streams, on the caller's connection.
///
/// Entries claimed but never acknowledged are re-delivered once they have been
/// pending for the visibility timeout, so `release` is a no-op. The store has
/// no capacity bound. Needs Redis 6.2 or later (`XAUTOCLAIM`).
pub struct RedisOutbox {
    conn: ConnectionManager,
    key: String,
    visibility: Duration,
    consumer: String,
    ready: OnceCell<()>,
}

impl RedisOutbox {
    /// A store on `conn` with key `santati:outbox` and a 60 s visibility
    /// timeout.
    pub fn new(conn: ConnectionManager) -> RedisOutbox {
        RedisOutbox {
            conn,
            key: "santati:outbox".to_string(),
            visibility: Duration::from_millis(60_000),
            consumer: uuid::Uuid::new_v4().to_string(),
            ready: OnceCell::new(),
        }
    }

    /// The stream's key.
    pub fn key(mut self, key: impl Into<String>) -> RedisOutbox {
        self.key = key.into();
        self
    }

    /// How long a claimed entry stays invisible before another claim may
    /// re-deliver it.
    pub fn visibility(mut self, visibility: Duration) -> RedisOutbox {
        self.visibility = visibility;
        self
    }

    async fn ensure_group(&self) -> Result<(), Error> {
        self.ready
            .get_or_try_init(|| async {
                let mut conn = self.conn.clone();
                let created = redis::cmd("XGROUP")
                    .arg("CREATE")
                    .arg(&self.key)
                    .arg(GROUP)
                    .arg("0")
                    .arg("MKSTREAM")
                    .query_async::<()>(&mut conn)
                    .await;
                match created {
                    Err(error) if !error.to_string().contains("BUSYGROUP") => {
                        Err(unavailable(error))
                    }
                    _ => Ok(()),
                }
            })
            .await
            .map(|_| ())
    }

    async fn drop_entries(&self, ids: &[String]) -> Result<(), Error> {
        if ids.is_empty() {
            return Ok(());
        }
        let mut conn = self.conn.clone();
        redis::cmd("XACK")
            .arg(&self.key)
            .arg(GROUP)
            .arg(ids)
            .query_async::<i64>(&mut conn)
            .await
            .map_err(unavailable)?;
        redis::cmd("XDEL")
            .arg(&self.key)
            .arg(ids)
            .query_async::<i64>(&mut conn)
            .await
            .map_err(unavailable)?;
        Ok(())
    }
}

fn unavailable(error: redis::RedisError) -> Error {
    Error::outbox("store_unavailable", error.to_string())
}

#[async_trait]
impl OutboxStore for RedisOutbox {
    async fn enqueue(&self, event: EventInput) -> Result<(), Error> {
        let json = serde_json::to_string(&event)
            .map_err(|error| Error::outbox("store_unavailable", error.to_string()))?;
        self.ensure_group().await?;
        let mut conn = self.conn.clone();
        redis::cmd("XADD")
            .arg(&self.key)
            .arg("*")
            .arg("event")
            .arg(json)
            .query_async::<String>(&mut conn)
            .await
            .map_err(unavailable)?;
        Ok(())
    }

    async fn claim(&self, limit: usize) -> Result<Vec<OutboxEntry>, Error> {
        if limit == 0 {
            return Ok(Vec::new());
        }
        self.ensure_group().await?;
        let mut conn = self.conn.clone();

        let reply = redis::cmd("XAUTOCLAIM")
            .arg(&self.key)
            .arg(GROUP)
            .arg(&self.consumer)
            .arg(self.visibility.as_millis() as u64)
            .arg("0-0")
            .arg("COUNT")
            .arg(limit)
            .query_async::<Value>(&mut conn)
            .await
            .map_err(unavailable)?;
        let mut raw = match &reply {
            Value::Array(parts) => parts.get(1).map(parse_entries).unwrap_or_default(),
            _ => Vec::new(),
        };

        if raw.len() < limit {
            let reply = redis::cmd("XREADGROUP")
                .arg("GROUP")
                .arg(GROUP)
                .arg(&self.consumer)
                .arg("COUNT")
                .arg(limit - raw.len())
                .arg("STREAMS")
                .arg(&self.key)
                .arg(">")
                .query_async::<Value>(&mut conn)
                .await
                .map_err(unavailable)?;
            raw.extend(parse_read_reply(&reply));
        }

        let mut entries = Vec::with_capacity(raw.len());
        let mut poison = Vec::new();
        for (id, json) in raw {
            match json.and_then(|json| serde_json::from_str::<EventInput>(&json).ok()) {
                Some(event) => entries.push(OutboxEntry { id, event }),
                None => poison.push(id),
            }
        }
        self.drop_entries(&poison).await?;
        Ok(entries)
    }

    async fn ack(&self, ids: Vec<String>) -> Result<(), Error> {
        self.drop_entries(&ids).await
    }

    async fn release(&self, _ids: Vec<String>) -> Result<(), Error> {
        Ok(())
    }
}

fn text(value: &Value) -> Option<String> {
    match value {
        Value::BulkString(bytes) => Some(String::from_utf8_lossy(bytes).into_owned()),
        Value::SimpleString(text) => Some(text.clone()),
        _ => None,
    }
}

/// `[[id, [field, value, ...]], ...]` into `(id, event field)`; nil or
/// deleted placeholders are skipped.
fn parse_entries(value: &Value) -> Vec<(String, Option<String>)> {
    let Value::Array(items) = value else {
        return Vec::new();
    };
    items
        .iter()
        .filter_map(|item| {
            let Value::Array(pair) = item else {
                return None;
            };
            let id = text(pair.first()?)?;
            let event = match pair.get(1) {
                Some(Value::Array(fields)) => fields
                    .chunks(2)
                    .find(|chunk| chunk.first().and_then(text).as_deref() == Some("event"))
                    .and_then(|chunk| chunk.get(1))
                    .and_then(text),
                Some(Value::Map(fields)) => fields
                    .iter()
                    .find(|(name, _)| text(name).as_deref() == Some("event"))
                    .and_then(|(_, value)| text(value)),
                _ => None,
            };
            Some((id, event))
        })
        .collect()
}

/// `XREADGROUP`'s reply: nil, `[[key, entries]]` (RESP2) or `{key: entries}`
/// (RESP3).
fn parse_read_reply(value: &Value) -> Vec<(String, Option<String>)> {
    match value {
        Value::Array(streams) => streams
            .iter()
            .filter_map(|stream| match stream {
                Value::Array(pair) => pair.get(1).map(parse_entries),
                _ => None,
            })
            .flatten()
            .collect(),
        Value::Map(streams) => streams
            .iter()
            .flat_map(|(_, entries)| parse_entries(entries))
            .collect(),
        _ => Vec::new(),
    }
}
