import assert from "node:assert/strict";
import { test } from "node:test";

import { MemoryOutbox, OutboxError, Santati } from "./index.js";
import type { EventInput, SantatiOptions, SendOutcome } from "./index.js";

const client = (store: MemoryOutbox, options: Partial<SantatiOptions> = {}): Santati =>
  new Santati({
    apiKey: "sat_sk_x",
    baseUrl: "http://127.0.0.1:1",
    outbox: store,
    flushIntervalMs: 60000,
    ...options,
  });

test("an unexpected send failure is reported and dropped", async () => {
  const store = new MemoryOutbox();
  const outcomes: SendOutcome[] = [];
  const santati = client(store, {
    // Not an event: emitBatch fails on it with a TypeError, not a SantatiError.
    preSend: () =>
      ({
        get event(): string {
          throw new TypeError("boom");
        },
      }) as unknown as EventInput,
    postSend: (_event, outcome) => {
      outcomes.push(outcome);
    },
  });

  await santati.log({ event: "a.b", trail: "t" });
  await santati.flush();

  assert.equal(outcomes.length, 1);
  assert.equal(outcomes[0]?.status, "failed");
  assert.ok(outcomes[0]?.error instanceof OutboxError);
  assert.equal(outcomes[0]?.error.code, "hook_failed");
  assert.deepEqual(await store.claim(10), []);
  await santati.close();
});

test("log stores a snapshot of the event", async () => {
  const store = new MemoryOutbox();
  const santati = client(store);
  const metadata = { a: "1" };

  await santati.log({ event: "a.b", trail: "t", metadata });
  metadata.a = "2";

  assert.deepEqual((await store.claim(1))[0]?.event.metadata, { a: "1" });
  await santati.close();
});
