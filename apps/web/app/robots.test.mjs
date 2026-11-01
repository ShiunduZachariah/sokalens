import assert from "node:assert/strict";
import { readFileSync } from "node:fs";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const HERE = path.dirname(fileURLToPath(import.meta.url));
const SOURCE = readFileSync(path.resolve(HERE, "robots.ts"), "utf8");

/**
 * robots.txt decides who may read the site, and some entries in it are
 * load-bearing in ways that stay invisible until revenue or traffic stops.
 *
 * The file allowed everyone until 2026-09-21, when a traffic audit found
 * Amazonbot taking 32.9% of origin requests, Meta 30.6%, and Googlebot 0.3% --
 * forty-six requests against Amazonbot's 4,771. Narrowing it was right, but
 * the same edit is one careless line away from blocking a crawler the business
 * depends on, and nothing would fail. No error, no alert, no red test. AdSense
 * simply stops paying, or pages fall out of the index over the following weeks.
 *
 * So this asserts the negative: those agents are never disallowed.
 *
 * It parses the rule blocks rather than matching a regex across the whole
 * file. The first version of this test did the latter, and its pattern lost a
 * level of backslash escaping through the template literal -- `\s` became `s`
 * -- so it matched nothing and passed against a deliberately broken input. A
 * guard that cannot fail is worse than no guard, because it is also a claim
 * that someone checked.
 */

/**
 * Splits robots.ts into one entry per rule block.
 *
 * Deliberately crude: the file is a small literal that this repo owns, so a
 * split on the key is enough and is far easier to reason about than a parser.
 */
function ruleBlocks(source) {
  return source
    .split(/userAgent\s*:/)
    .slice(1)
    .map((chunk) => {
      const name = /^\s*["'`]([^"'`]+)["'`]/.exec(chunk);
      // Stop at the end of this rule object so a later rule's `disallow` is
      // never read as belonging to this one.
      const body = chunk.split("}")[0] ?? "";
      return { agent: name ? name[1] : null, body };
    })
    .filter((rule) => rule.agent !== null);
}

/** Agents that must keep access, and what breaks if they lose it. */
const MUST_NOT_BE_BLOCKED = [
  ["Googlebot", "search indexing -- the entire product depends on being found"],
  ["Bingbot", "Bing and Copilot search traffic"],
  ["Google-Adstxt", "reads /ads.txt; blocking it fails AdSense site approval"],
  ["Mediapartners-Google", "serves AdSense; blocking it stops ad revenue"],
  ["facebookexternalhit", "link previews on Facebook and WhatsApp, which is how match links spread"],
  ["AhrefsBot", "the operator's own SEO reporting, which only covers sites it may crawl"],
];

test("the block parser actually distinguishes allow from disallow", () => {
  // Guarding the guard. The previous version of this file passed against a
  // disallowed Googlebot, so the parser earns its own assertion before
  // anything is trusted to it.
  const parsed = ruleBlocks('{ userAgent: "Googlebot", disallow: "/" }, { userAgent: "Bingbot", allow: "/" }');
  assert.equal(parsed.length, 2, "parser did not find both rules");
  assert.equal(parsed[0].agent, "Googlebot");
  assert.ok(parsed[0].body.includes("disallow"), "parser missed a disallow it must catch");
  assert.equal(parsed[1].agent, "Bingbot");
  assert.ok(!parsed[1].body.includes("disallow"), "parser reported a disallow on an allowed agent");
});

test("no crawler the business depends on is ever disallowed", () => {
  // Asserts the agent is never *blanket* blocked, not that the word
  // "disallow" is absent from its block.
  //
  // The earlier version asserted the latter, and it was the same over-strict
  // shape already corrected on the wildcard rule: it forbade the ordinary,
  // correct use of robots.txt for keeping a crawler off a query parameter.
  // That mattered as soon as Googlebot got an explicit group, because a
  // crawler obeys only its most specific match -- so `/*?slip=` has to be
  // repeated inside the Googlebot block, and the old assertion called that
  // rule a business-breaking block.
  //
  // What actually breaks the business is losing the site, or a whole section
  // of it. So that is what is checked: no `/`, and nothing short enough to be
  // a section sweep rather than a parameter rule.
  const rules = ruleBlocks(SOURCE);
  for (const [agent, why] of MUST_NOT_BE_BLOCKED) {
    for (const rule of rules) {
      if (rule.agent.toLowerCase() !== agent.toLowerCase()) continue;
      const disallows = [...rule.body.matchAll(/disallow:\s*"([^"]*)"/g)].map((m) => m[1]);
      for (const path of disallows) {
        assert.notEqual(path, "/",
          `${agent} is disallowed from the whole site in robots.ts -- this breaks ${why}`);
        assert.ok(path.length > 1,
          `the disallow "${path}" on ${agent} is too broad to be a parameter rule -- this risks ${why}`);
      }
    }
  }
});

test("an explicitly named crawler still carries the wildcard's parameter rules", () => {
  // The precedence trap, guarded.
  //
  // robots.txt gives a crawler only its most specific matching group, so the
  // moment an agent is named explicitly it stops inheriting the wildcard
  // entirely. Every parameter rule the wildcard carries has to be repeated in
  // that agent's own block or it silently stops applying -- to the one crawler
  // it was written for, with no error and no visible change to the file.
  const rules = ruleBlocks(SOURCE);
  const wildcard = rules.find((rule) => rule.agent === "*");
  const wildcardDisallows = [...wildcard.body.matchAll(/disallow:\s*"([^"]*)"/g)].map((m) => m[1]);

  // Agents that index. Fetch-only agents are exempt: Mediapartners-Google
  // targets ads on whatever page shows them and indexes nothing, and
  // Google-Adstxt reads a single file, so neither spends crawl budget.
  const INDEXING = ["Googlebot", "Googlebot-Image"];
  for (const agent of INDEXING) {
    const rule = rules.find((r) => r.agent === agent);
    if (!rule) continue;
    const own = [...rule.body.matchAll(/disallow:\s*"([^"]*)"/g)].map((m) => m[1]);
    for (const path of wildcardDisallows) {
      assert.ok(own.includes(path),
        `the wildcard disallows "${path}" but ${agent} has its own group and does not repeat it, `
        + `so the rule no longer applies to ${agent} at all`);
    }
  }
});

test("the default rule still allows everything not named", () => {
  // A new search engine must be findable by default. Flipping the wildcard to
  // a disallow would make the site invisible to everything not on a hand-kept
  // allowlist, which is the opposite of the goal.
  const wildcard = ruleBlocks(SOURCE).find((rule) => rule.agent === "*");
  assert.ok(wildcard, "robots.ts no longer has a wildcard rule");
  assert.match(
    wildcard.body,
    /allow:\s*"\/"/,
    "the wildcard rule no longer allows /; every unnamed crawler, including new search engines, would be blocked",
  );
  // A narrow disallow is allowed here and a blanket one is not. The rule this
  // guards is "a new search engine can find the site", not "this block may
  // never contain the word disallow" -- the earlier version asserted the
  // latter, which would have blocked the ordinary use of robots.txt for
  // keeping crawlers off a parameter.
  const disallows = [...wildcard.body.matchAll(/disallow:\s*"([^"]*)"/g)].map((m) => m[1]);
  for (const path of disallows) {
    assert.notEqual(path, "/",
      "the wildcard rule disallows / outright; the site would be invisible by default");
    assert.ok(path.length > 1,
      `the wildcard disallow "${path}" is too broad to be a parameter rule`);
  }
});

test("the crawlers the audit identified are actually restricted", () => {
  // The other direction. If these come off the list the audit's finding
  // silently reverts and the catalogue is being given away again.
  const rules = ruleBlocks(SOURCE);
  for (const agent of ["Amazonbot", "meta-externalagent"]) {
    const rule = rules.find((r) => r.agent === agent);
    assert.ok(rule, `${agent} is no longer named in robots.ts; it took a third of origin traffic and sent no readers`);
    assert.ok(rule.body.includes("disallow"), `${agent} is named but no longer disallowed`);
  }
});

test("a sitemap is still advertised", () => {
  // robots.txt is where crawlers look for it. Losing this line costs discovery
  // of every URL not already linked from a crawled page.
  assert.match(SOURCE, /sitemap:/i, "robots.ts no longer advertises a sitemap");
});
