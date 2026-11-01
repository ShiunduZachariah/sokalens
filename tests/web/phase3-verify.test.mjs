import assert from "node:assert/strict";
import { mkdtemp, mkdir, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import test from "node:test";

import { runStaticChecks } from "../../apps/web/scripts/phase3-verify.mjs";

const routeExpectations = [
  {
    route: "/",
    file: "app/page.tsx",
    metadataFile: "app/layout.tsx",
    metadata: "layout",
    titleIncludes: ["SokaLens"],
    descriptionIncludes: ["Football"],
  },
  {
    route: "/football",
    file: "app/football/page.tsx",
    metadata: "static",
    titleIncludes: ["Football", "SokaLens"],
    descriptionIncludes: ["fixtures"],
  },
  {
    route: "/match/[slug]",
    file: "app/match/[slug]/page.tsx",
    metadata: "dynamic",
    titleIncludes: ["Match", "SokaLens"],
    descriptionIncludes: ["prediction"],
    requiresStaticParams: true,
  },
];

async function withFixture(callback) {
  const root = await mkdtemp(join(tmpdir(), "phase3-verify-"));

  try {
    await callback(root);
  } finally {
    await rm(root, { force: true, recursive: true });
  }
}

async function writeFixture(root, relativePath, contents) {
  const absolutePath = join(root, relativePath);
  await mkdir(join(absolutePath, ".."), { recursive: true });
  await writeFile(absolutePath, contents, "utf8");
}

test("static verifier accepts route metadata and responsible disclaimer wording", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = {
        title: "SokaLens - Football analysis",
        description: "Football predictions, fixtures, and match intelligence.",
      };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `export default function Page() {
        return <main>Predictions are informational and not guaranteed.</main>;
      }`,
    );
    await writeFixture(
      root,
      "app/football/page.tsx",
      `export const metadata = {
        title: "Football fixtures - SokaLens",
        description: "Football fixtures and responsible predictions.",
      };`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export function generateStaticParams() {
        return [{ slug: "arsenal-vs-chelsea" }];
      }
      export async function generateMetadata() {
        return {
          title: "Match analysis - SokaLens",
          description: "Match prediction and odds context.",
        };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.deepEqual(result.failures, []);
  });
});

test("static verifier permits the Best Bets product label without permitting certainty claims", async () => {
  await withFixture(async (root) => {
    const bestBetsExpectation = [
      {
        route: "/best-bets",
        file: "app/best-bets/page.tsx",
        metadata: "static",
        titleIncludes: ["Best Bets", "SokaLens"],
        descriptionIncludes: ["predictions", "confidence"],
      },
    ];

    await writeFixture(
      root,
      "app/best-bets/page.tsx",
      `export const metadata = {
        title: "Best Bets | SokaLens",
        description: "Strong football predictions ranked by model confidence.",
      };
      export default function Page() {
        return <main><h1>Best Bets</h1><p>Predictions are statistical estimates, not guaranteed outcomes.</p></main>;
      }`,
    );

    const allowedResult = runStaticChecks({ root, routeExpectations: bestBetsExpectation });
    assert.deepEqual(allowedResult.failures, []);

    await writeFixture(
      root,
      "app/best-bets/page.tsx",
      `export const metadata = {
        title: "Best Bets | SokaLens",
        description: "Strong football predictions ranked by model confidence.",
      };
      export default function Page() {
        return <main><h1>Best Bets</h1><p>Guaranteed wins with risk-free returns.</p></main>;
      }`,
    );

    const rejectedResult = runStaticChecks({ root, routeExpectations: bestBetsExpectation });
    assert.ok(
      rejectedResult.failures.some((failure) => failure.includes("guaranteed outcome")),
    );
    assert.ok(rejectedResult.failures.some((failure) => failure.includes("no-risk phrase")));
  });
});

test("static verifier reports missing metadata, direct providers, and certainty wording", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = { title: "SokaLens" };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `export default function Page() {
        return <main>Guaranteed win today from API-Football.</main>;
      }`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export async function generateMetadata() {
        return { title: "Match analysis - SokaLens" };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.ok(
      result.failures.some((failure) => failure.includes("Missing required route file: /football")),
    );
    assert.ok(
      result.failures.some((failure) => failure.includes("Missing metadata description")),
    );
    assert.ok(
      result.failures.some((failure) => failure.includes("Direct provider reference")),
    );
    assert.ok(
      result.failures.some((failure) => failure.includes("Banned certainty wording")),
    );
  });
});

test("static verifier reports filter chips and tabs that only jump to page anchors", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = {
        title: "SokaLens - Football analysis",
        description: "Football predictions, fixtures, and match intelligence.",
      };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `export default function Page() {
        return (
          <main>
            <a className="filter-chip" href="#content">All</a>
            <a className="tab" href="#analysis">Odds</a>
          </main>
        );
      }`,
    );
    await writeFixture(
      root,
      "app/football/page.tsx",
      `export const metadata = {
        title: "Football fixtures - SokaLens",
        description: "Football fixtures and responsible predictions.",
      };`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export function generateStaticParams() {
        return [{ slug: "arsenal-vs-chelsea" }];
      }
      export async function generateMetadata() {
        return {
          title: "Match analysis - SokaLens",
          description: "Match prediction and odds context.",
        };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.ok(
      result.failures.some((failure) => failure.includes("Dead interactive anchor")),
    );
  });
});

test("static verifier reports public web layout and copy polish regressions", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = {
        title: "SokaLens - Football analysis",
        description: "Football predictions, fixtures, and match intelligence.",
      };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `export default function Page() {
        return <main className="wide-shell">MVP sample copy for local review. Reserved ad space.</main>;
      }`,
    );
    await writeFixture(
      root,
      "app/components/PublicHeader.tsx",
      `import Link from "next/link";
      const navItems = [
        { label: "Football", href: "/football" },
        { label: "Predictions", href: "/predictions/today" },
      ];
      export function PublicHeader() {
        return <nav>{navItems.map((item) => <Link href={item.href}>{item.label}</Link>)}</nav>;
      }`,
    );
    await writeFixture(
      root,
      "app/components/FilterBar.tsx",
      `"use client";
      import { useState } from "react";
      export function FilterBar({ active }) {
        const [selected, setSelected] = useState(active);
        return <button aria-pressed={selected === active} onClick={() => setSelected(active)}>All</button>;
      }`,
    );
    await writeFixture(
      root,
      "app/pro/page.tsx",
      `export const metadata = {
        title: "SokaLens Pro",
        description: "No ads and deeper analysis.",
      };
      export default function ProPage() {
        return <section><article className="card">Pro Monthly<a className="button" href="#waitlist">Join</a></article></section>;
      }`,
    );
    await writeFixture(
      root,
      "app/components/account/PlansAndCheckout.tsx",
      `export function PlansAndCheckout() {
        return <article className="card">Monthly plan</article>;
      }`,
    );
    await writeFixture(
      root,
      "app/football/page.tsx",
      `export const metadata = {
        title: "Football fixtures - SokaLens",
        description: "Football fixtures and responsible predictions.",
      };`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export function generateStaticParams() {
        return [{ slug: "arsenal-vs-chelsea" }];
      }
      export async function generateMetadata() {
        return {
          title: "Match analysis - SokaLens",
          description: "Match prediction and odds context.",
        };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.ok(result.failures.some((failure) => failure.includes("Draft public copy")));
    assert.ok(result.failures.some((failure) => failure.includes("Primary nav missing")));
    assert.ok(result.failures.some((failure) => failure.includes("Primary nav active state")));
    assert.ok(result.failures.some((failure) => failure.includes("FilterBar active prop sync")));
    assert.ok(result.failures.some((failure) => failure.includes("Pro plan cards")));
    assert.ok(result.failures.some((failure) => failure.includes("Recommended Pro plan")));
  });
});

test("static verifier rejects direct provider widget wrappers", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = {
        title: "SokaLens - Football analysis",
        description: "Football predictions, fixtures, and match intelligence.",
      };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `import { ApiSportsWidget } from "./components/ApiSportsWidget";
      export default function Page() {
        return <main><ApiSportsWidget title="Live football" widget="fixtures" /></main>;
      }`,
    );
    await writeFixture(
      root,
      "app/components/ApiSportsWidget.tsx",
      `import Script from "next/script";
      export function ApiSportsWidget() {
        return (
          <section>
            <Script src="https://widgets.api-sports.io/3.1.0/widgets.js" strategy="lazyOnload" />
            <api-sports-widget data-key="restricted-widget-key" data-sport="football" data-refresh="60" data-show-logos="false" />
          </section>
        );
      }`,
    );
    await writeFixture(
      root,
      "app/football/page.tsx",
      `export const metadata = {
        title: "Football fixtures - SokaLens",
        description: "Football fixtures and responsible predictions.",
      };`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export function generateStaticParams() {
        return [{ slug: "arsenal-vs-chelsea" }];
      }
      export async function generateMetadata() {
        return {
          title: "Match analysis - SokaLens",
          description: "Match prediction and odds context.",
        };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.ok(result.failures.some((failure) => failure.includes("API-Sports widget script")));
    assert.ok(result.failures.some((failure) => failure.includes("API-Sports widget element")));
  });
});

test("static verifier blocks ad hoc provider widgets and frontend provider secrets", async () => {
  await withFixture(async (root) => {
    await writeFixture(
      root,
      "app/layout.tsx",
      `export const metadata = {
        title: "SokaLens - Football analysis",
        description: "Football predictions, fixtures, and match intelligence.",
      };`,
    );
    await writeFixture(
      root,
      "app/page.tsx",
      `export default function Page() {
        const secret = process.env.API_FOOTBALL_KEY;
        return (
          <main>
            <script src="https://widgets.api-sports.io/3.1.0/widgets.js" />
            <api-sports-widget data-key={secret} data-sport="football" />
          </main>
        );
      }`,
    );
    await writeFixture(
      root,
      "app/football/page.tsx",
      `export const metadata = {
        title: "Football fixtures - SokaLens",
        description: "Football fixtures and responsible predictions.",
      };`,
    );
    await writeFixture(
      root,
      "app/match/[slug]/page.tsx",
      `export function generateStaticParams() {
        return [{ slug: "arsenal-vs-chelsea" }];
      }
      export async function generateMetadata() {
        return {
          title: "Match analysis - SokaLens",
          description: "Match prediction and odds context.",
        };
      }`,
    );

    const result = runStaticChecks({ root, routeExpectations });

    assert.ok(
      result.failures.some((failure) => failure.includes("Direct provider reference")),
    );
    assert.ok(
      result.failures.some((failure) => failure.includes("Frontend provider secret")),
    );
  });
});
