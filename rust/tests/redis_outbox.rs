//! The Redis adapter against a real server; needs `SANTATI_TEST_REDIS_URL`.
#![cfg(feature = "redis")]

use std::time::Duration;

use santati::{EventInput, OutboxStore, RedisOutbox};

fn event(n: u32) -> EventInput {
    EventInput {
        event: format!("e{n}"),
        trail: Some("t".into()),
        idempotency_key: Some(format!("k{n}")),
        ..Default::default()
    }
}

#[tokio::test]
async fn redis_outbox_claims_acks_and_redelivers_stale_entries(
) -> Result<(), Box<dyn std::error::Error>> {
    let Ok(url) = std::env::var("SANTATI_TEST_REDIS_URL") else {
        eprintln!("SANTATI_TEST_REDIS_URL not set; skipping the Redis outbox test");
        return Ok(());
    };
    eprintln!("running the Redis outbox test against {url}");
    let conn = redis::Client::open(url)?.get_connection_manager().await?;
    let key = format!("santati:test:{}", uuid::Uuid::new_v4());

    let result = exercise(conn.clone(), &key).await;
    let _: () = redis::cmd("DEL")
        .arg(&key)
        .query_async(&mut conn.clone())
        .await?;
    result
}

async fn exercise(
    conn: redis::aio::ConnectionManager,
    key: &str,
) -> Result<(), Box<dyn std::error::Error>> {
    let store = RedisOutbox::new(conn.clone()).key(key);
    for n in 1..=3 {
        store.enqueue(event(n)).await?;
    }

    let first = store.claim(2).await?;
    let names: Vec<&str> = first.iter().map(|e| e.event.event.as_str()).collect();
    assert_eq!(names, ["e1", "e2"]);
    assert!(first.iter().all(|e| !e.id.is_empty()));
    assert_ne!(first[0].id, first[1].id);

    let rest = store.claim(5).await?;
    assert_eq!(rest.len(), 1);
    assert_eq!(rest[0].event, event(3));
    assert!(store.claim(5).await?.is_empty());

    store
        .ack(vec![first[0].id.clone(), first[1].id.clone()])
        .await?;

    let other = RedisOutbox::new(conn.clone())
        .key(key)
        .visibility(Duration::ZERO);
    let stale = other.claim(10).await?;
    assert_eq!(stale.len(), 1);
    assert_eq!(stale[0].event, event(3));

    other.ack(vec![rest[0].id.clone()]).await?;
    assert!(store.claim(5).await?.is_empty());
    assert!(other.claim(5).await?.is_empty());
    let len: i64 = redis::cmd("XLEN")
        .arg(key)
        .query_async(&mut conn.clone())
        .await?;
    assert_eq!(len, 0);
    Ok(())
}
