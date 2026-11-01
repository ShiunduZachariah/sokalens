import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

test("builder explains partial and fully expired tickets without stale totals", async () => {
  const source = await readFile(new URL("./BuilderClient.tsx", import.meta.url), "utf8");
  const styles = await readFile(new URL("../globals.css", import.meta.url), "utf8");

  assert.match(source, /role="status"/);
  assert.match(source, /Some selections were removed/);
  assert.match(source, /This shared slip has expired/);
  assert.match(source, /expiredBuilderLegs/);

  // The notice must be styled, or it renders as two unseparated sentences in the
  // middle of the ticket and reads as part of it. `.builder-expiry-notice` until
  // the 2026-10-02 Builder refresh renamed it `.bd-notice`; the property guarded
  // is unchanged, which is why this is re-pointed rather than dropped.
  assert.match(styles, /\.bd-notice \{/, "the expiry notice has no style rule");
  assert.match(
    styles,
    /\.bd-notice--full \{/,
    "the fully-expired variant has no style rule; it is the one case where nothing on the page works",
  );
});
