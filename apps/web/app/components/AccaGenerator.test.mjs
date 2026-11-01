import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";

const source = () => readFile(path.join(import.meta.dirname, "AccaGenerator.tsx"), "utf8");

test("the search answers within a bounded wait, on a phone as much as a desktop", async () => {
  // A spinner that never resolves is worse than a clear no: on a phone it
  // costs battery and data to keep asking, and the reader cannot tell whether
  // the page is working or broken.
  const s = await source();

  const budget = Number(s.match(/ACCA_SEARCH_BUDGET_MS = ([0-9_]+)/)?.[1].replace(/_/g, ""));
  const attempt = Number(s.match(/ACCA_ATTEMPT_TIMEOUT_MS = ([0-9_]+)/)?.[1].replace(/_/g, ""));
  const delay = Number(s.match(/ACCA_RETRY_DELAY_MS = ([0-9_]+)/)?.[1].replace(/_/g, ""));

  assert.ok(budget > 0 && budget <= 10_000,
    `total wait ${budget}ms must be bounded and no more than 10s`);
  // Above the API's own 2750ms ceiling, or an attempt is cut off just as the
  // server is about to answer.
  assert.ok(attempt > 2_750, `per-attempt timeout ${attempt}ms cuts off a server about to answer`);
  assert.ok(attempt < budget, "a single attempt must not be able to consume the whole budget");
  // Room for at least two attempts, or the retry is decorative.
  assert.ok(attempt + delay + attempt <= budget + delay,
    "the budget must fit more than one attempt");
});

test("a stalled request cannot consume the whole budget", async () => {
  // Without a per-attempt abort, one hung connection on a bad network eats the
  // entire wait and the retry never happens -- which is exactly the case this
  // is built for.
  const s = await source();
  assert.match(s, /new AbortController\(\)/);
  assert.match(s, /signal: controller\.signal/);
  assert.match(s, /controller\.abort\(\)/);
});

test("the wait explains itself once a first attempt comes back empty", async () => {
  // "Searching…" forever reads as a hang. Saying that nothing is stored yet and
  // one is being built is the difference between waiting and wondering.
  const s = await source();
  // `stillLooking` existing as state proves nothing -- it survives in the
  // declaration and in a comment when the message itself is gone, which is how a
  // rewrite dropped this once. Assert the rendered branch.
  assert.match(
    s,
    /\{stillLooking\s*\n?\s*\?/,
    "stillLooking is no longer used to choose the message; the wait has one state again",
  );
  assert.match(s, /building one/i, "the second message is gone");
  assert.match(s, /Searching the catalogue/, "the first message is gone");
  assert.match(s, /aria-live="polite"/, "the change of state must reach a screen reader");
});

test("reduced motion is respected", async () => {
  const css = await readFile(
    path.join(import.meta.dirname, "..", "globals.css"), "utf8");
  const block = css.slice(css.indexOf(".gn-spinner"));
  assert.ok(block.length > 0, "the generator spinner has no styles at all");
  assert.match(block, /prefers-reduced-motion: reduce/);
});

test("a composed ACCA can be kept", async () => {
  // It exists only in this response -- not in the browsable catalogue, gone on
  // refresh. The old copy told the reader to "copy it to the Builder" by hand,
  // which is a task the page can do for them.
  const s = await source();
  assert.match(s, /addBuilderLegs\(bundleLegs\(bundle\)\)/,
    "the generator must add legs the same way the catalogue does");
  // "ticket" rather than "Builder" since the merge: the generator now lives
  // inside the Builder and the panel it adds to is labelled "Your ticket", so
  // "add to Builder" would name the page the reader is already on. The design
  // carries the leg count in that label, and the verb has to stay "add":
  // `addBuilderLegs` appends, so a label promising to replace the ticket would
  // describe something this does not do.
  assert.match(s, /Add these \$\{bundle\.legs\.length\}/);
  assert.match(s, /to my ticket/);
  assert.match(s, /Added to ticket/, "the button must confirm it worked");
});

test("builder legs are mapped by the shared helper, not a second copy", async () => {
  // bundleLegs sets groupId, source and oddsType, which the Builder reads to
  // group and price a slip. A duplicated mapping would drift from it silently.
  const s = await source();
  assert.match(s, /import \{ bundleLegs \}/);
  assert.doesNotMatch(s, /predictionId: leg\.id/,
    "the leg mapping must not be duplicated here");
});
