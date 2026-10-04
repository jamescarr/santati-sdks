import type { Redis } from "ioredis";

import { envelopeFromWire, envelopeToWire } from "./client.js";
import { OutboxError, SantatiError } from "./errors.js";
import type { EventInput, OutboxEntry, OutboxStore } from "./types.js";

const GROUP = "santati";

export interface RedisOutboxOptions {
  /** The stream key; default `santati:outbox`. */
  key?: string;
  /** How long a claimed entry stays invisible before it is re-delivered; default 60000. */
  visibilityMs?: number;
}

/**
 * An `OutboxStore` on a Redis stream and one consumer group. Takes the
 * caller's ioredis client; the SDK never opens connections. The layout is the
 * same in every Santati SDK, so any of them can drain what another enqueued.
 */
export class RedisOutbox implements OutboxStore {
  private readonly key: string;
  private readonly visibilityMs: number;
  private readonly consumer = crypto.randomUUID();
  private ready: Promise<void> | undefined;

  constructor(
    private readonly client: Redis,
    options: RedisOutboxOptions = {},
  ) {
    this.key = options.key ?? "santati:outbox";
    this.visibilityMs = options.visibilityMs ?? 60_000;
  }

  async enqueue(event: EventInput): Promise<void> {
    await this.wrap(async () => {
      await this.init();
      await this.client.xadd(this.key, "*", "event", JSON.stringify(envelopeToWire(event)));
    });
  }

  async claim(limit: number): Promise<OutboxEntry[]> {
    if (limit <= 0) return [];
    return this.wrap(async () => {
      await this.init();
      const raw: unknown[] = [];
      const poison: string[] = [];
      const reclaimed = (await this.client.call(
        "XAUTOCLAIM",
        this.key,
        GROUP,
        this.consumer,
        String(this.visibilityMs),
        "0-0",
        "COUNT",
        String(limit),
      )) as unknown[];
      if (Array.isArray(reclaimed) && Array.isArray(reclaimed[1])) raw.push(...reclaimed[1]);
      const entries = this.parse(raw, poison);
      const missing = limit - raw.filter((item) => Array.isArray(item)).length;
      if (missing > 0) {
        const read = (await this.client.call(
          "XREADGROUP",
          "GROUP",
          GROUP,
          this.consumer,
          "COUNT",
          String(missing),
          "STREAMS",
          this.key,
          ">",
        )) as unknown;
        if (Array.isArray(read) && Array.isArray(read[0]) && Array.isArray(read[0][1])) {
          entries.push(...this.parse(read[0][1] as unknown[], poison));
        }
      }
      if (poison.length > 0) await this.remove(poison);
      return entries;
    });
  }

  async ack(ids: string[]): Promise<void> {
    if (ids.length === 0) return;
    await this.wrap(async () => {
      await this.init();
      await this.remove(ids);
    });
  }

  /** A no-op: unacked entries are re-delivered after `visibilityMs`. */
  release(_ids: string[]): void {}

  /** `[id, [field, value, …]]` items; deleted placeholders (nil) are skipped. */
  private parse(items: unknown[], poison: string[]): OutboxEntry[] {
    const entries: OutboxEntry[] = [];
    for (const item of items) {
      if (!Array.isArray(item) || typeof item[0] !== "string") continue;
      const id = item[0];
      const fields = Array.isArray(item[1]) ? (item[1] as unknown[]) : [];
      let json: unknown;
      for (let i = 0; i + 1 < fields.length; i += 2) {
        if (fields[i] === "event") json = fields[i + 1];
      }
      try {
        if (typeof json !== "string") throw new Error("no event field");
        entries.push({ id, event: envelopeFromWire(JSON.parse(json)) });
      } catch {
        poison.push(id);
      }
    }
    return entries;
  }

  private async remove(ids: string[]): Promise<void> {
    await this.client.xack(this.key, GROUP, ...ids);
    await this.client.xdel(this.key, ...ids);
  }

  private init(): Promise<void> {
    this.ready ??= this.client
      .call("XGROUP", "CREATE", this.key, GROUP, "0", "MKSTREAM")
      .then(
        () => undefined,
        (error: unknown) => {
          if (error instanceof Error && error.message.includes("BUSYGROUP")) return;
          this.ready = undefined;
          throw error;
        },
      );
    return this.ready;
  }

  private async wrap<T>(run: () => Promise<T>): Promise<T> {
    try {
      return await run();
    } catch (error) {
      if (error instanceof SantatiError) throw error;
      throw new OutboxError(error instanceof Error ? error.message : String(error), {
        code: "store_unavailable",
      });
    }
  }
}
