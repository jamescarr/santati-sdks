import assert from "node:assert/strict";
import { test } from "node:test";

import { Santati, ValidationError } from "./index.js";

// The generated core throws its own `RequiredError` for a parameter it needs;
// the facade turns it into a ValidationError before any request is made.
test("a missing version is a ValidationError, not a generated-core error", async () => {
  const santati = new Santati({ apiKey: "sat_sk_x", baseUrl: "http://127.0.0.1:1" });
  await assert.rejects(
    () => santati.schemas.getVersion("invoice.voided", undefined as unknown as number),
    (error: unknown) =>
      error instanceof ValidationError && error.field === "version" && error.status === null,
  );
});
