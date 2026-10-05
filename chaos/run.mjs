// Load and fault-injection run of every SDK's outbox against a chaos gateway.
// Node ESM, stdlib only. `mise run chaos [pkgs…]` calls this.
//
//   CHAOS_SCENARIOS=blackhole,flaky node chaos/run.mjs python go
//
// For each SDK and scenario: start a gateway, launch the SDK's driver with the
// scenario's config in SANTATI_CHAOS, parse the driver's `CHAOS_RESULT` line,
// and judge it against the scenario's budgets. The gateway lives in this
// process, so drivers are spawned asynchronously: a synchronous spawn would
// freeze the gateway's event loop and every request it holds.
import { spawn, spawnSync } from "node:child_process";
import { mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";

import { startGateway } from "./gateway.mjs";

const ROOT = new URL("..", import.meta.url);
const CHAOS = new URL("./", import.meta.url);
const readJson = (url) => JSON.parse(readFileSync(url, "utf8"));

const sdksFile = readJson(new URL("sdks.json", CHAOS));
const plan = readJson(new URL("scenarios.json", CHAOS));

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function deepMerge(base, over) {
  const out = { ...base };
  for (const [key, value] of Object.entries(over ?? {})) {
    out[key] = isObject(value) && isObject(out[key]) ? deepMerge(out[key], value) : value;
  }
  return out;
}

// ---- selection ------------------------------------------------------------

const selected = process.argv.slice(2);
for (const name of selected) {
  if (!(name in sdksFile.sdks)) {
    console.log(`chaos/sdks.json has no SDK "${name}" (known: ${Object.keys(sdksFile.sdks).sort().join(", ")})`);
    process.exit(1);
  }
}
const sdkNames = selected.length > 0 ? selected : Object.keys(sdksFile.sdks).sort();

const wanted = (process.env.CHAOS_SCENARIOS ?? "")
  .split(",")
  .map((name) => name.trim())
  .filter(Boolean);
for (const name of wanted) {
  if (!(name in plan.scenarios)) {
    console.log(`chaos/scenarios.json has no scenario "${name}" (known: ${Object.keys(plan.scenarios).join(", ")})`);
    process.exit(1);
  }
}
const scenarioNames = wanted.length > 0 ? wanted : Object.keys(plan.scenarios);

// ---- one run ----------------------------------------------------------------

function drive(sdk, cwd, config) {
  return new Promise((resolve) => {
    const child = spawn(sdk.run[0], sdk.run.slice(1), {
      cwd,
      env: { ...process.env, ...sdk.env, SANTATI_CHAOS: JSON.stringify(config) },
      stdio: ["ignore", "pipe", "inherit"],
    });
    let stdout = "";
    let timedOut = false;
    let settled = false;
    child.stdout.on("data", (chunk) => {
      stdout += chunk;
    });
    const timer = setTimeout(() => {
      timedOut = true;
      child.kill("SIGKILL");
    }, config.driver_timeout_ms);
    const finish = (code) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve({ stdout, code, timedOut });
    };
    child.on("error", (error) => {
      stdout += `\nspawn failed: ${error.message}\n`;
      finish(1);
    });
    child.on("close", (code) => finish(code));
  });
}

function parseResult(stdout) {
  for (const line of stdout.split("\n")) {
    const text = line.trimStart();
    if (!text.startsWith("CHAOS_RESULT ")) continue;
    try {
      return JSON.parse(text.slice("CHAOS_RESULT ".length));
    } catch {
      return null;
    }
  }
  return null;
}

function judge(result, stats, budgets) {
  const violations = [];
  if (result.emit_ms.p99 > budgets.emit_p99_ms) {
    violations.push(`emit p99 ${result.emit_ms.p99}ms > ${budgets.emit_p99_ms}ms`);
  }
  if (result.emit_ms.max > budgets.emit_max_ms) {
    violations.push(`emit max ${result.emit_ms.max}ms > ${budgets.emit_max_ms}ms`);
  }
  if (result.heartbeat_max_lag_ms !== null && result.heartbeat_max_lag_ms > budgets.heartbeat_max_lag_ms) {
    violations.push(`heartbeat lag ${result.heartbeat_max_lag_ms}ms > ${budgets.heartbeat_max_lag_ms}ms`);
  }
  if (result.caller_exits > 0) violations.push(`caller_exits ${result.caller_exits}`);
  const allowed = new Set(budgets.allow_errors ?? []);
  for (const [key, count] of Object.entries(result.errors ?? {})) {
    if (!allowed.has(key)) violations.push(`unexpected error ${key} x${count}`);
  }
  for (const key of budgets.require_errors ?? []) {
    if (!(key in (result.errors ?? {}))) violations.push(`required error ${key} never seen`);
  }
  if (budgets.close_ms !== undefined && result.close_ms > budgets.close_ms) {
    violations.push(`close ${result.close_ms}ms > ${budgets.close_ms}ms`);
  }
  if (budgets.deliver_all && stats.delivered !== result.queued) {
    violations.push(`delivered ${stats.delivered} != queued ${result.queued}`);
  }
  if (budgets.deliver_none && stats.delivered > 0) {
    violations.push(`delivered ${stats.delivered}, expected none`);
  }
  return violations;
}

async function runScenario(sdkName, sdk, cwd, scenarioName) {
  const scenario = plan.scenarios[scenarioName];
  const gateway = await startGateway(scenario.gateway);
  let config = deepMerge(plan.defaults, scenario.config);
  if (sdk.profile === "php") config = deepMerge(config, plan.php_overrides);
  config.client.base_url = gateway.url;
  // A profile entry (e.g. php) can relax a budget where an SDK's documented behaviour
  // differs, and says why; the note is printed and kept in the results.
  const profile = scenario.profiles?.[sdk.profile];
  const budgets = { ...plan.budgets, ...scenario.budgets, ...profile?.budgets };

  const violations = [];
  let result = null;
  let stats;
  try {
    const run = await drive(sdk, cwd, config);
    if (run.timedOut) violations.push("driver timed out (hang)");
    result = parseResult(run.stdout);
    if (result === null) {
      violations.push(`no result (exit ${run.code})`);
      const tail = run.stdout.trim().split("\n").slice(-5).join("\n");
      if (tail) console.log(tail.replace(/^/gm, "    | "));
    }
  } finally {
    stats = gateway.stats();
    await gateway.close();
  }
  if (result !== null) violations.push(...judge(result, stats, budgets));
  return { sdk: sdkName, scenario: scenarioName, result, stats, violations, note: profile?.note };
}

// ---- main -------------------------------------------------------------------

const fmt = (value) => (value === null || value === undefined ? "-" : String(value));
const rows = [];

console.log(["sdk", "scenario", "p99", "max", "lag", "close", "queued/delivered", "errors", "verdict"].join("\t"));

for (const sdkName of sdkNames) {
  const sdk = sdksFile.sdks[sdkName];
  const cwd = fileURLToPath(new URL(`${sdkName}/`, ROOT));

  let setupFailed = false;
  for (const [command, ...args] of sdk.setup ?? []) {
    const done = spawnSync(command, args, { cwd, stdio: "inherit" });
    if (done.status !== 0) {
      setupFailed = true;
      break;
    }
  }

  for (const scenarioName of scenarioNames) {
    const row = setupFailed
      ? { sdk: sdkName, scenario: scenarioName, result: null, stats: null, violations: ["setup failed"] }
      : await runScenario(sdkName, sdk, cwd, scenarioName);
    rows.push(row);

    const { result, stats, violations } = row;
    const errors = result ? Object.entries(result.errors ?? {}).map(([k, v]) => `${k}=${v}`).join(",") : "";
    console.log(
      [
        sdkName,
        scenarioName,
        fmt(result?.emit_ms.p99),
        fmt(result?.emit_ms.max),
        fmt(result?.heartbeat_max_lag_ms),
        fmt(result?.close_ms),
        `${fmt(result?.queued)}/${fmt(stats?.delivered)}`,
        errors || "-",
        violations.length === 0 ? "PASS" : `FAIL ${violations.join("; ")}`,
      ].join("\t"),
    );
    if (row.note) console.log(`    note: ${row.note}`);
  }
}

mkdirSync(new URL(".results/", CHAOS), { recursive: true });
writeFileSync(new URL(".results/latest.json", CHAOS), `${JSON.stringify(rows, null, 2)}\n`);

const failed = rows.filter((row) => row.violations.length > 0).length;
console.log(`${rows.length - failed}/${rows.length} pass`);
process.exit(failed > 0 ? 1 : 0);
