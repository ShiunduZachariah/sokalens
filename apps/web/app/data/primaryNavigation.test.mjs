import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const appRoot = new URL("../", import.meta.url);
const expectedDesktopItems = [
  ["nav.home", "/"],
  ["nav.predictions", "/predictions/today"],
  ["nav.in_play", "/in-play"],
  ["nav.accas", "/acca-predictions"],
  ["nav.builder", "/multi-match-builder"],
];

// Six slots, matching the design export (Version 28, 5 Oct 2026): the five
// primary destinations plus the account utility, which MobileBottomNav appends
// at render time because its label and href depend on the session.
//
// In-play was absent here until 2026-10-06. The earlier assertion recorded that
// as "mobile uses the fifth slot for account", but it gave no reason for the
// asymmetry with desktop -- and the effect was that the live surface had no
// entry point in mobile primary navigation at all, including on /in-play
// itself, on the breakpoint where live football is actually watched.
const expectedMobileItems = [
  ["nav.home", "/"],
  ["nav.predictions", "/predictions/today"],
  ["nav.in_play", "/in-play"],
  ["nav.accas", "/acca-predictions"],
  ["nav.builder", "/multi-match-builder"],
];

function primaryItems(source) {
  return [...source.matchAll(/\{ label: "(nav\.[^"]+)", href: "([^"]+)"(?:, icon: "[^"]+")? \}/g)]
    .map((match) => [match[1], match[2]]);
}

test("desktop and mobile carry the same five destinations, with account appended on mobile", async () => {
  const desktop = await readFile(new URL("components/PublicHeader.tsx", appRoot), "utf8");
  const mobile = await readFile(new URL("components/MobileBottomNav.tsx", appRoot), "utf8");
  assert.deepEqual(primaryItems(desktop), expectedDesktopItems);
  assert.deepEqual(primaryItems(mobile), expectedMobileItems);
  assert.doesNotMatch(mobile, /nav\.search|icon: "search"/);
  assert.match(mobile, /authenticated \? "\/account" : "\/login"/);
  assert.match(mobile, /icon: "account"/);
  assert.doesNotMatch(`${desktop}\n${mobile}`, /nav\.fixtures|href: "\/matches\/today"/);
});

test("the account link stays a utility outside the primary landmark", async () => {
  const source = await readFile(new URL("components/PublicHeader.tsx", appRoot), "utf8");
  const primaryNav = source.match(/<nav className="header-nav"[\s\S]*?<\/nav>/)?.[0];
  assert.ok(primaryNav, "PublicHeader must retain a primary navigation landmark");
  // The landmark is for navigating the site. An upgrade prompt and an account
  // link are utilities, and putting them inside it makes the landmark lie to
  // anyone moving by landmark. This is the assertion worth keeping.
  assert.doesNotMatch(primaryNav, /pro-link|account-link|nav\.upgrade|nav\.account/);
  assert.match(source, /className="account-link"/);
});

test("Go Pro is absent from the header entirely", async () => {
  // It used to live in a .header-utility-links wrapper, which this file
  // asserted the existence of. Hiding the link left that wrapper empty, so it
  // went too -- and an assertion that an empty container exists protects
  // nothing. What is worth pinning is that the link itself is gone from the
  // header, so it cannot quietly return without this test being read.
  const source = await readFile(new URL("components/PublicHeader.tsx", appRoot), "utf8");
  assert.doesNotMatch(source, /nav\.upgrade|pro-link/);
});
