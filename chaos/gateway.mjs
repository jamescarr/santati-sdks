// A fault-injecting stand-in for the ingest endpoint. Node ESM, stdlib only.
//
//   const gw = await startGateway({ mode: "flaky", seed: 42 });
//   gw.url      // base_url for the SDK under test
//   gw.stats()  // { requests, outcomes: { "202", "503", "429", "reset", "hung" }, delivered }
//   await gw.close()
//
// Modes: healthy, slow (delay_ms), blackhole (never answers), refused (nothing
// listens), flaky (seeded mix of 503 / reset / slow / 429 / healthy), recovery
// (blackhole for the first down_ms, healthy after).
import { createServer } from "node:http";

// mulberry32: a tiny seeded PRNG, so a `flaky` run is reproducible.
function mulberry32(seed) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const EVENTS_PATH = "/api/v0/events/";

export async function startGateway(spec) {
  const mode = spec.mode;
  const stats = { requests: 0, outcomes: { 202: 0, 503: 0, 429: 0, reset: 0, hung: 0 } };
  const delivered = new Set();

  if (mode === "refused") {
    // Reserve a port, then release it: connecting to it is refused.
    const probe = createServer();
    await new Promise((resolve) => probe.listen(0, "127.0.0.1", resolve));
    const { port } = probe.address();
    await new Promise((resolve) => probe.close(resolve));
    return {
      url: `http://127.0.0.1:${port}`,
      stats: () => ({ requests: 0, outcomes: { ...stats.outcomes }, delivered: 0 }),
      close: async () => {},
    };
  }

  const random = mulberry32(spec.seed ?? 42);
  const startedAt = Date.now();
  let nextId = 0;
  const sockets = new Set();
  const timers = new Set();

  const later = (ms, fn) => {
    const timer = setTimeout(() => {
      timers.delete(timer);
      fn();
    }, ms);
    timers.add(timer);
  };

  const healthy = (res, events) => {
    const results = events.map((event, index) => {
      if (event && typeof event.idempotency_key === "string") delivered.add(event.idempotency_key);
      nextId += 1;
      return { index, status: "accepted", id: `evt_${nextId}` };
    });
    stats.outcomes[202] += 1;
    if (res.destroyed || res.writableEnded) return;
    res.writeHead(202, { "content-type": "application/json" });
    res.end(JSON.stringify({ accepted: events.length, rejected: 0, results }));
  };

  const hang = (req) => {
    stats.outcomes.hung += 1;
    // Held until close() destroys the socket; nothing is ever written.
    req.on("error", () => {});
  };

  const unavailable = (res) => {
    stats.outcomes[503] += 1;
    res.writeHead(503, { "content-type": "application/json" });
    res.end(JSON.stringify({ error: { code: "unavailable", message: "chaos" } }));
  };

  const rateLimited = (res) => {
    stats.outcomes[429] += 1;
    res.writeHead(429, { "content-type": "application/json", "retry-after": "1" });
    res.end(JSON.stringify({ error: { code: "rate_limited", message: "chaos" } }));
  };

  const answer = (req, res, events) => {
    switch (mode) {
      case "healthy":
        return healthy(res, events);
      case "slow":
        return later(spec.delay_ms ?? 0, () => healthy(res, events));
      case "blackhole":
        return hang(req);
      case "recovery":
        return Date.now() - startedAt < (spec.down_ms ?? 0) ? hang(req) : healthy(res, events);
      case "flaky": {
        const r = random();
        if (r < 0.25) return unavailable(res);
        if (r < 0.35) {
          stats.outcomes.reset += 1;
          return req.socket.destroy();
        }
        if (r < 0.5) return later(800, () => healthy(res, events));
        if (r < 0.6) return rateLimited(res);
        return healthy(res, events);
      }
      default:
        throw new Error(`unknown gateway mode "${mode}"`);
    }
  };

  const server = createServer((req, res) => {
    stats.requests += 1;
    if (req.method !== "POST" || req.url !== EVENTS_PATH) {
      req.resume();
      res.writeHead(404, { "content-type": "application/json" });
      res.end(JSON.stringify({ error: { code: "not_found", message: "chaos" } }));
      return;
    }
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("error", () => {});
    req.on("end", () => {
      let events;
      try {
        const body = JSON.parse(Buffer.concat(chunks).toString("utf8"));
        if (!body || !Array.isArray(body.events)) throw new Error("no events");
        events = body.events;
      } catch {
        res.writeHead(400, { "content-type": "application/json" });
        res.end(JSON.stringify({ error: { code: "invalid_request", message: "chaos" } }));
        return;
      }
      answer(req, res, events);
    });
  });
  server.on("connection", (socket) => {
    sockets.add(socket);
    socket.on("close", () => sockets.delete(socket));
    socket.on("error", () => {});
  });

  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  const { port } = server.address();

  return {
    url: `http://127.0.0.1:${port}`,
    stats: () => ({
      requests: stats.requests,
      outcomes: Object.fromEntries(Object.entries(stats.outcomes).map(([k, v]) => [k, v])),
      delivered: delivered.size,
    }),
    close: async () => {
      for (const timer of timers) clearTimeout(timer);
      timers.clear();
      for (const socket of sockets) socket.destroy();
      await new Promise((resolve) => server.close(resolve));
    },
  };
}
