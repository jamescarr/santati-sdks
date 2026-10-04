import { OutboxError, RateLimitedError, SantatiError, ServerError, TransportError, ValidationError } from "./errors.js";
import type {
  BatchResult,
  EventInput,
  OutboxEntry,
  OutboxStore,
  PostSendHook,
  PreSendHook,
  SendOutcome,
} from "./types.js";

const DEFAULT_MAX_PENDING = 10_000;

/** A bounded in-memory `OutboxStore`. Lost when the process exits. */
export class MemoryOutbox implements OutboxStore {
  private readonly maxPending: number;
  private pending: OutboxEntry[] = [];
  private readonly claimed = new Map<string, OutboxEntry>();
  private counter = 0;

  constructor(options: { maxPending?: number } = {}) {
    const maxPending = options.maxPending ?? DEFAULT_MAX_PENDING;
    if (!Number.isInteger(maxPending) || maxPending < 1) {
      throw new ValidationError("maxPending must be at least 1", { field: "max_pending" });
    }
    this.maxPending = maxPending;
  }

  enqueue(event: EventInput): void {
    if (this.pending.length + this.claimed.size >= this.maxPending) {
      throw new OutboxError(`outbox is full (${this.maxPending} pending)`, { code: "outbox_full" });
    }
    this.pending.push({ id: String(++this.counter), event });
  }

  claim(limit: number): OutboxEntry[] {
    if (limit <= 0) return [];
    const taken = this.pending.splice(0, limit);
    for (const entry of taken) this.claimed.set(entry.id, entry);
    return taken;
  }

  ack(ids: string[]): void {
    for (const id of ids) this.claimed.delete(id);
  }

  release(ids: string[]): void {
    const back: OutboxEntry[] = [];
    for (const id of ids) {
      const entry = this.claimed.get(id);
      if (entry === undefined) continue;
      this.claimed.delete(id);
      back.push(entry);
    }
    this.pending.unshift(...back);
  }
}

/** The one method of `Events` the worker needs. */
export interface BatchSender {
  emitBatchWithStatus(events: EventInput[], retries?: boolean): Promise<{ result: BatchResult; status: number }>;
}

function messageOf(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function storeFailure(error: unknown): SantatiError {
  if (error instanceof SantatiError) return error;
  return new OutboxError(messageOf(error), { code: "store_unavailable" });
}

function retryable(error: SantatiError): boolean {
  return (
    error instanceof TransportError ||
    error instanceof ServerError ||
    error instanceof RateLimitedError
  );
}

/** The client's outbox side: a queued `emit`, the background tick and the pass shared with `flush`. */
export class OutboxWorker {
  private closed = false;
  private timer: NodeJS.Timeout | undefined;
  private started = false;
  private chain: Promise<void> = Promise.resolve();

  constructor(
    private readonly events: BatchSender,
    private readonly store: OutboxStore,
    private readonly batchSize: number,
    private readonly flushIntervalMs: number,
    private readonly preSend?: PreSendHook,
    private readonly postSend?: PostSendHook,
  ) {}

  /** Stores the already-resolved event; never makes a request. */
  async enqueue(stored: EventInput): Promise<void> {
    if (this.closed) throw new OutboxError("client is closed", { code: "closed" });
    try {
      await this.store.enqueue(stored);
    } catch (error) {
      throw storeFailure(error);
    }
    if (!this.started) {
      this.started = true;
      this.arm();
    }
  }

  /** One pass, serialised with the worker's own passes. */
  flush(): Promise<void> {
    this.chain = this.chain.then(
      () => this.pass(),
      () => this.pass(),
    );
    return this.chain;
  }

  async close(): Promise<void> {
    if (this.closed) return;
    this.closed = true;
    if (this.timer !== undefined) clearTimeout(this.timer);
    await this.flush();
  }

  private arm(): void {
    this.timer = setTimeout(() => {
      // Store failures and anything unexpected are swallowed: the next tick retries.
      void this.flush()
        .catch(() => {})
        .then(() => {
          if (!this.closed) this.arm();
        });
    }, this.flushIntervalMs);
    this.timer.unref();
  }

  private async guard<T>(call: () => T | Promise<T>): Promise<T> {
    try {
      return await call();
    } catch (error) {
      throw storeFailure(error);
    }
  }

  private async notify(event: EventInput, outcome: SendOutcome): Promise<void> {
    if (this.postSend === undefined) return;
    try {
      await this.postSend(event, outcome);
    } catch {
      // A post_send exception never affects delivery.
    }
  }

  private async pass(): Promise<void> {
    for (;;) {
      const entries = await this.guard(() => this.store.claim(this.batchSize));
      if (entries.length === 0) return;
      let released = false;
      const toSend: { entry: OutboxEntry; out: EventInput }[] = [];
      for (const entry of entries) {
        let out: EventInput | null | undefined = entry.event;
        if (this.preSend !== undefined) {
          try {
            out = await this.preSend(structuredClone(entry.event));
          } catch (error) {
            await this.guard(() => this.store.release([entry.id]));
            released = true;
            await this.notify(entry.event, {
              status: "failed",
              error: new OutboxError(messageOf(error), { code: "hook_failed" }),
            });
            continue;
          }
        }
        if (out == null) {
          await this.guard(() => this.store.ack([entry.id]));
          continue;
        }
        toSend.push({ entry, out });
      }
      if (toSend.length > 0) {
        const ids = toSend.map(({ entry }) => entry.id);
        let sent: { result: BatchResult; status: number } | undefined;
        try {
          sent = await this.events.emitBatchWithStatus(
            toSend.map(({ out }) => out),
            false,
          );
        } catch (error) {
          if (!(error instanceof SantatiError)) {
            // E.g. a malformed preSend result: reported and dropped, never thrown from flush()/close().
            const failure = new OutboxError(messageOf(error), { code: "hook_failed" });
            for (const { entry } of toSend) {
              await this.notify(entry.event, { status: "failed", error: failure });
            }
            await this.guard(() => this.store.ack(ids));
          } else {
            for (const { entry } of toSend) {
              await this.notify(entry.event, { status: "failed", error });
            }
            if (retryable(error)) {
              await this.guard(() => this.store.release(ids));
              released = true;
            } else {
              await this.guard(() => this.store.ack(ids));
            }
          }
        }
        if (sent !== undefined) {
          const status = sent.status;
          for (const item of sent.result.results) {
            const target = toSend[item.index];
            if (target === undefined) continue;
            if (item.status === "rejected") {
              await this.notify(target.entry.event, {
                status: "rejected",
                error: new ValidationError(item.error?.message ?? "rejected", {
                  status,
                  code: item.error?.code,
                  field: item.error?.field,
                }),
              });
            } else {
              await this.notify(target.entry.event, { status: item.status, id: item.id });
            }
          }
          await this.guard(() => this.store.ack(ids));
        }
      }
      if (released || entries.length < this.batchSize) return;
    }
  }
}
