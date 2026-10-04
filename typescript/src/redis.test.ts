import assert from "node:assert/strict";
import { randomUUID } from "node:crypto";
import { test } from "node:test";

import { Redis } from "ioredis";

import type { EventInput } from "./types.js";
import { RedisOutbox } from "./redis.js";

const url = process.env.SANTATI_TEST_REDIS_URL;

const event = (i: number): EventInput => ({
  event: `e${i}`,
  trail: "t",
  idempotencyKey: `k${i}`,
});

test("RedisOutbox claims FIFO, acks and redelivers stale entries", { skip: !url }, async () => {
  const client = new Redis(url as string);
  const key = `santati:test:${randomUUID()}`;
  try {
    const store = new RedisOutbox(client, { key });
    for (const i of [1, 2, 3]) await store.enqueue(event(i));

    const first = await store.claim(2);
    assert.deepEqual(first.map((e) => e.event), [event(1), event(2)]);
    assert.ok(first.every((e) => e.id !== ""));
    assert.equal(new Set(first.map((e) => e.id)).size, 2);

    const second = await store.claim(5);
    assert.deepEqual(second.map((e) => e.event), [event(3)]);
    assert.deepEqual(await store.claim(5), []);

    await store.ack(first.map((e) => e.id));

    const other = new RedisOutbox(client, { key, visibilityMs: 0 });
    const stale = await other.claim(10);
    assert.deepEqual(stale.map((e) => e.event), [event(3)]);

    await other.ack(stale.map((e) => e.id));
    assert.deepEqual(await store.claim(5), []);
    assert.deepEqual(await other.claim(5), []);
    assert.equal(await client.xlen(key), 0);
  } finally {
    await client.del(key);
    await client.quit();
  }
});
