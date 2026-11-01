import assert from "node:assert/strict";
import test from "node:test";
import { readFile } from "node:fs/promises";
import path from "node:path";

const appDir = path.resolve(import.meta.dirname, "..");
const read = (relative) => readFile(path.join(appDir, relative), "utf8");

const PAGE = "acca-predictions/slip/[id]/page.tsx";
const DETAIL = "acca-predictions/AccaSlipDetail.tsx";

/*
 * The ACCA detail page, pinned where it is quietly breakable.
 *
 * Every assertion here is for something that leaves a page which renders
 * perfectly and is nonetheless wrong: a cached slip stuck mid-match, a clock
 * read during render, a generated slip linked to a URL that cannot exist.
 */

test("the by-id read is uncached, because leg statuses change during play", async () => {
  const api = await read("data/accaApi.ts");
  assert.match(api, /fetchPredictionApi\(`\/v1\/accas\/\$\{encodeURIComponent\(id\)\}`, \{ revalidate: 0 \}\)/,
    "the slip read must not be cached: the API serves it from its uncached source precisely because a leg's grade changes while the match is being played");
  assert.match(await read(PAGE), /export const dynamic = "force-dynamic"/);
});

test("a missing slip is a 404 and an outage is not", async () => {
  const source = await read(PAGE);
  // The order matters: notFound() inside the catch would turn an API outage into
  // a signal to search engines that these URLs are gone.
  assert.match(source, /\} catch \{[\s\S]*?return \(\s*<ContentPageShell/,
    "a failed read must render the shell, not notFound()");
  assert.match(source, /if \(!slip\) notFound\(\);/);
  const api = await read("data/accaApi.ts");
  assert.match(api, /if \(response\.status === 404\) return null;/);
});

test("the clock is read in the page, never during render", async () => {
  const page = await read(PAGE);
  const detail = await read(DETAIL);
  assert.match(page, /accaSlipView\(slip, Date\.now\(\)\)/);
  // A clock read inside the client component hydrates differently from the
  // server's own read a moment earlier, and the state that depends on it
  // (live vs pending) is exactly what the banner reports.
  assert.doesNotMatch(detail, /Date\.now\(\)/,
    "the detail islands render the view they are handed; reading the clock here is a hydration mismatch");
  // A call, not the name: the component's own comment explains why the view is
  // computed on the server, and a bare /accaSlipView/ matches that comment.
  assert.doesNotMatch(detail, /accaSlipView\(/);
});

test("status pills appear only once there is something to report", async () => {
  const detail = await read(DETAIL);
  assert.match(detail, /const showStatuses = view\.result !== null;/);
  assert.match(detail, /showStatuses \? \(\s*<span className=\{`as-pill as-pill--\$\{leg\.state\}`\}>/,
    "a column of 'Not started' on a slip that has not kicked off repeats what the kick-off times already say");
});

test("both islands read the odds format from the shared hook", async () => {
  const detail = await read(DETAIL);
  // Two islands, one preference. Component state in either would let the legs
  // and the combined figure be drawn in different formats on the same page.
  assert.equal((detail.match(/useOddsFormat\(\)/g) ?? []).length, 2);
  assert.doesNotMatch(detail, /useState<OddsFormat>/);
});

test("the segmented format control sets the same preference as the select elsewhere", async () => {
  const detail = await read(DETAIL);
  assert.match(detail, /aria-pressed=\{format === key\}/);
  assert.match(detail, /onClick=\{\(\) => setFormat\(key\)\}/,
    "the group must persist through the hook, not local state, or it will disagree with the account preference");
});

test("only a stored slip is linked to a slip URL", async () => {
  const card = await read("acca-predictions/AccaBundleCard.tsx");
  assert.match(card, /bundle\.origin === "on_demand" \|\| !bundle\.id \? bundle\.label/,
    "an on-demand slip is never written down, so /acca-predictions/slip/{id} 404s on it");
  assert.match(card, /\/acca-predictions\/slip\/\$\{encodeURIComponent\(bundle\.id\)\}/);
});

test("the slip page lives under a static segment, away from the market slugs", async () => {
  // `/acca-predictions/[marketSlug]` already owns that level. Two dynamic
  // segments at one level is not expressible, and branching inside
  // [marketSlug] would send every mistyped market slug to a slip lookup.
  const page = await read(PAGE);
  assert.match(page, /const path = `\/acca-predictions\/slip\/\$\{slip\.id\}`/);
  // Counted rather than matched: the not-found branch of generateMetadata also
  // sets index:false, so a single match passes even when the real slip's
  // metadata has been flipped to index:true.
  assert.equal((page.match(/index: false/g) ?? []).length, 2,
    "a generated ticket that expires when its last match ends is not an indexable page; follow stays on for the fixture links");
  assert.doesNotMatch(page, /index: true/);
});

test("the share link still carries the slip token", async () => {
  // The URL resolves on its own now, but a social crawler draws the card from
  // the query string without reaching the API.
  const page = await read(PAGE);
  assert.match(page, /\?slip=\$\{encodeURIComponent\(encodeSlipToken\(\{/);
});
