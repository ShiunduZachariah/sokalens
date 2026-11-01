import assert from "node:assert/strict";
import test from "node:test";
import { readFile, readdir } from "node:fs/promises";
import path from "node:path";

const webRoot = path.resolve(import.meta.dirname, "../../..");
const appDir = path.join(webRoot, "app");

async function sources(dir) {
  const out = [];
  for (const entry of await readdir(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name);
    if (entry.isDirectory()) out.push(...await sources(full));
    else if (/\.tsx$/.test(entry.name)) out.push(full);
  }
  return out;
}

const css = await readFile(path.join(appDir, "globals.css"), "utf8");
const files = await sources(appDir);

/**
 * The refreshed pages are styled entirely by `mp-` classes in globals.css. A
 * typo in a class name is invisible at build time and in `tsc` -- the element
 * simply renders unstyled, which on a dark-ground page usually means invisible
 * text rather than an obvious break. This is the cheapest place to catch it.
 */
test("every mp- class used in a component is defined in globals.css", async () => {
  const defined = new Set([...css.matchAll(/\.(mp-[a-zA-Z0-9_-]+)/g)].map((m) => m[1]));
  const missing = new Map();

  for (const file of files) {
    const source = await readFile(file, "utf8");
    for (const match of source.matchAll(/(?:className|class)=(?:"([^"]*)"|\{`([^`]*)`\}|\{"([^"]*)"\})/g)) {
      const raw = match[1] ?? match[2] ?? match[3] ?? "";
      for (const token of raw.split(/[\s${}?:'"]+/)) {
        if (!token.startsWith("mp-") || defined.has(token)) continue;
        // A trailing hyphen is the stump of a dynamically suffixed class --
        // `mp-chip--${result}` splits to "mp-chip--". The suffixes are asserted
        // separately below, where the real set is known.
        if (token.endsWith("-")) continue;
        if (!missing.has(token)) missing.set(token, path.relative(webRoot, file));
      }
    }
  }

  assert.deepEqual([...missing.entries()], [],
    "undefined mp- classes (class -> first file using it)");
});

/**
 * Both themes or neither. The design's light artboards are the same source
 * rendered with theme = light, so a token that exists in only one block is a
 * page that breaks in the other theme -- silently, and only for the readers
 * using it.
 */
test("every --sl- token is defined in both the light and the dark block", () => {
  // Selector-anchored matching is not enough on its own: this stylesheet has a
  // pre-existing :root[data-theme="dark"] block for the legacy --color-* tokens,
  // and it comes first in the file. Pick the block that actually holds --sl-.
  const block = (pattern, label) => {
    const blocks = [...css.matchAll(pattern)]
      .map((m) => new Set([...m[1].matchAll(/(--sl-[a-z0-9-]+):/g)].map((t) => t[1])))
      .filter((set) => set.has("--sl-bg"));
    assert.equal(blocks.length, 1, `expected exactly one --sl- ${label} block, found ${blocks.length}`);
    return blocks[0];
  };

  const light = block(/:root,\r?\n:root\[data-theme="light"\] \{([\s\S]*?)\r?\n\}/g, "light");
  const dark = block(/:root\[data-theme="dark"\] \{([\s\S]*?)\r?\n\}/g, "dark");
  const fallback = block(/:root:not\(\[data-theme="light"\]\):not\(\[data-theme="dark"\]\) \{([\s\S]*?)\r?\n  \}/g, "no-JS fallback");

  assert.deepEqual([...light].sort(), [...dark].sort(),
    "a token defined for one theme but not the other");
  assert.deepEqual([...dark].sort(), [...fallback].sort(),
    "the no-JS dark fallback must carry the same tokens as the attribute block");
});

/**
 * Accent fill and accent ink must stay distinct in light mode. The lime reads
 * at 13.6:1 on dark but only 3.3:1 as text on white, so collapsing them is how
 * the light theme ships failing contrast.
 */
test("light mode keeps accent fill and accent ink apart", () => {
  const light = [...css.matchAll(/:root,\r?\n:root\[data-theme="light"\] \{([\s\S]*?)\r?\n\}/g)]
    .map((m) => m[1])
    .find((body) => body.includes("--sl-bg"));
  assert.ok(light, "light --sl- token block not found");
  const value = (name) => light.match(new RegExp(`${name}:\\s*([^;]+);`))?.[1].trim();

  assert.notEqual(value("--sl-accent"), value("--sl-accent-ink"),
    "light mode needs a darker accent for text than for fills");
  assert.notEqual(value("--sl-away"), value("--sl-away-ink"),
    "same for the away-side accent");
});

/**
 * The W/D/L chips, whose classes are built as `mp-chip--${result}` and so are
 * invisible to the scan above.
 *
 * L is intentionally absent: a loss is the unmodified `.mp-chip` -- outlined,
 * muted, no fill. W and D must both exist, because a draw rendered with the
 * win's fill would state the wrong result.
 */
test("the form chips define a win and a draw variant", () => {
  assert.match(css, /\.mp-chip--W\s*\{/, "win chip must be styled");
  assert.match(css, /\.mp-chip--D\s*\{/, "draw chip must be styled");
  assert.match(css, /\.mp-chip\s*\{/, "the base chip carries the loss styling");
});

/**
 * The stylesheet's braces balance.
 *
 * Earned its place: a scripted edit to this 8,000-line file dragged in a stray
 * `body {` while restoring a rule, which nested every later rule one level
 * deeper. tsc saw nothing, `next build` exited 0, and all 469 tests passed --
 * the only symptom was a "Parsing CSS source code failed" warning buried in
 * build output, and the real damage was silent: an @keyframes inside a rule
 * block, and every selector after the break scoped to the wrong parent.
 */
test("globals.css has balanced braces", () => {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  let depth = 0;
  let line = 1;
  for (const character of stripped) {
    if (character === "\n") line += 1;
    else if (character === "{") depth += 1;
    else if (character === "}") {
      depth -= 1;
      assert.ok(depth >= 0, `unmatched closing brace near line ${line}`);
    }
  }
  assert.equal(depth, 0, `${depth} unclosed block(s); every later rule is nested wrongly`);
});

/**
 * Top-level at-rules stay at the top level.
 *
 * The same break shows up here first and more legibly: an @keyframes or @media
 * that has drifted inside a rule block is the signature of an unbalanced edit
 * above it.
 */
test("@keyframes and @media sit at the top level", () => {
  const stripped = css.replace(/\/\*[\s\S]*?\*\//g, "");
  let depth = 0;
  let line = 1;
  const nested = [];
  for (let index = 0; index < stripped.length; index += 1) {
    const character = stripped[index];
    if (character === "\n") line += 1;
    else if (character === "{") depth += 1;
    else if (character === "}") depth -= 1;
    else if (character === "@" && depth !== 0) {
      const word = stripped.slice(index, index + 10);
      if (word.startsWith("@keyframes") || word.startsWith("@media")) {
        nested.push(`${word.split(/[\s{]/)[0]} at line ${line} (depth ${depth})`);
      }
    }
  }
  assert.deepEqual(nested, [], "at-rules nested inside a rule block");
});
