// Validate conformance/ and run the registered SDKs against its vectors.
// Node ESM, stdlib only. `mise run check:conformance [pkgs…]` calls this.
//
// Four phases: (1) validate the corpus, (2) print what it holds, (3) validate
// the registry against the package directories, (4) run the selected SDKs.
import { spawnSync } from "node:child_process";
import { readdirSync, readFileSync, statSync } from "node:fs";
import { fileURLToPath } from "node:url";

const ROOT = new URL("..", import.meta.url);
const CONFORMANCE = new URL("conformance/", ROOT);

const readJson = (url) => JSON.parse(readFileSync(url, "utf8"));

const featuresFile = readJson(new URL("features.json", CONFORMANCE));
const sdksFile = readJson(new URL("sdks.json", CONFORMANCE));
const features = featuresFile.features ?? [];
const operations = new Set(featuresFile.operations ?? []);
const errorKinds = new Set(featuresFile.error_kinds ?? []);

const errors = [];

// ---- phase 1: the corpus -------------------------------------------------

const featureIds = new Set();
for (const feature of features) {
  if (featureIds.has(feature.id)) errors.push(`duplicate feature id "${feature.id}"`);
  featureIds.add(feature.id);
}

const seenCaseIds = new Set();
const caseCounts = new Map();
let caseCount = 0;

const caseFiles = readdirSync(new URL("cases/", CONFORMANCE))
  .filter((name) => name.endsWith(".json"))
  .sort();
for (const file of caseFiles) {
  const cases = readJson(new URL(`cases/${file}`, CONFORMANCE)).cases ?? [];
  cases.forEach((raw, i) => {
    caseCount += 1;
    const c = raw !== null && typeof raw === "object" ? raw : {};
    for (const field of ["id", "feature", "operation", "input", "expect"]) {
      if (!(field in c)) errors.push(`case #${i} (${file}): missing "${field}"`);
    }
    if (!("id" in c)) return;
    if (seenCaseIds.has(c.id)) errors.push(`duplicate case id "${c.id}" (${file})`);
    seenCaseIds.add(c.id);
    if ("feature" in c) {
      if (!featureIds.has(c.feature)) errors.push(`case "${c.id}" (${file}): unknown feature "${c.feature}"`);
      else caseCounts.set(c.feature, (caseCounts.get(c.feature) ?? 0) + 1);
    }
    if ("operation" in c && !operations.has(c.operation)) {
      errors.push(`case "${c.id}" (${file}): unknown operation "${c.operation}"`);
    }
    const input = c.input ?? {};
    if (typeof input !== "object" || input === null || Array.isArray(input)) {
      errors.push(`case "${c.id}" (${file}): "input" must be an object`);
    } else {
      if (typeof input.client !== "object" || input.client === null || typeof input.client.api_key !== "string") {
        errors.push(`case "${c.id}" (${file}): input.client must be an object with a string api_key`);
      }
      if (typeof input.gateway !== "object" || input.gateway === null) {
        errors.push(`case "${c.id}" (${file}): input.gateway must be an object`);
      }
    }
    if ("expect" in c) {
      const expect = c.expect ?? {};
      const hasOk = "ok" in expect;
      const hasError = "error" in expect;
      if (hasOk === hasError) {
        errors.push(`case "${c.id}" (${file}): expect must have exactly one of "ok" or "error"`);
      } else if (hasError && !errorKinds.has(expect.error?.kind)) {
        errors.push(`case "${c.id}" (${file}): expect.error.kind must be one of ${[...errorKinds].join(", ")}`);
      }
      if ("requests" in expect && !Array.isArray(expect.requests)) {
        errors.push(`case "${c.id}" (${file}): expect.requests must be an array`);
      }
    }
  });
}

for (const feature of features) {
  if (!caseCounts.has(feature.id)) errors.push(`feature "${feature.id}" has no cases`);
}

if (errors.length > 0) {
  const prefix = process.env.GITHUB_ACTIONS === "true" ? "::error::" : "";
  for (const message of errors) console.log(`${prefix}${message}`);
  process.exit(1);
}

// ---- phase 2: what the corpus holds --------------------------------------

console.log(`${features.length} features, ${caseCount} cases`);
for (const feature of features) {
  console.log(`  ${feature.id}  ${caseCounts.get(feature.id) ?? 0}`);
}

// ---- phase 3: the registry vs the package directories --------------------

const packageNames = readdirSync(ROOT, { withFileTypes: true })
  .filter((entry) => entry.isDirectory() && !entry.name.startsWith("."))
  .map((entry) => entry.name)
  .filter((name) =>
    ["pyproject.toml", "package.json", "go.mod", "Cargo.toml", "mix.exs", "composer.json"].some((file) =>
      statSync(new URL(`${name}/${file}`, ROOT), { throwIfNoEntry: false }),
    ) || readdirSync(new URL(`${name}/`, ROOT)).some((file) => file.endsWith(".gemspec")),
  );

// Packages that are in the repo but implement no event operation, so there is
// nothing to run the corpus against; each maps to the reason it is exempt.
const exempt = sdksFile.exempt ?? {};

const selected = process.argv.slice(2);
if (selected.length === 0) {
  for (const name of packageNames) {
    if (!(name in sdksFile.sdks) && !(name in exempt)) {
      errors.push(`${name}/ is a package but is not registered in conformance/sdks.json`);
    }
  }
  for (const name of Object.keys(sdksFile.sdks)) {
    if (!packageNames.includes(name)) {
      errors.push(`conformance/sdks.json registers ${name}, but ${name}/ is not a package directory`);
    }
  }
  for (const name of Object.keys(exempt)) {
    if (!packageNames.includes(name)) {
      errors.push(`conformance/sdks.json exempts ${name}, but ${name}/ is not a package directory`);
    }
    if (name in sdksFile.sdks) errors.push(`conformance/sdks.json both registers and exempts ${name}`);
  }
} else {
  for (const name of selected) {
    if (name in exempt) {
      if (!packageNames.includes(name)) errors.push(`SDK "${name}" is exempt but ${name}/ is not a package directory`);
    } else if (!(name in sdksFile.sdks)) {
      errors.push(`conformance/sdks.json has no SDK "${name}"`);
    } else if (!packageNames.includes(name)) {
      errors.push(`SDK "${name}" is registered but ${name}/ is not a package directory`);
    }
  }
}

if (errors.length > 0) {
  const prefix = process.env.GITHUB_ACTIONS === "true" ? "::error::" : "";
  for (const message of errors) console.log(`${prefix}${message}`);
  process.exit(1);
}

// ---- phase 4: run the selected SDKs --------------------------------------

const names = selected.length > 0 ? selected : Object.keys(sdksFile.sdks).sort();
let failed = false;
for (const name of names) {
  if (name in exempt) {
    console.log(`${name}  exempt: ${exempt[name]}`);
    continue;
  }
  const { setup = [], run } = sdksFile.sdks[name];
  const cwd = fileURLToPath(new URL(`${name}/`, ROOT));
  let code = 0;
  for (const [command, ...args] of [...setup, run]) {
    const result = spawnSync(command, args, { cwd, stdio: "inherit" });
    if (result.status !== 0) {
      code = result.status ?? 1;
      break;
    }
  }
  if (code === 0) {
    console.log(`${name}  pass`);
  } else {
    failed = true;
    console.log(`${name}  FAIL (exit ${code})`);
  }
}
process.exit(failed ? 1 : 0);
