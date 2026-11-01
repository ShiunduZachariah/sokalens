import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

const root = new URL("./", import.meta.url);
const css = fs.readFileSync(new URL("globals.css", root), "utf8");

/*
 * The defect this exists to stop recurring.
 *
 * Review defect UI-07 was header buttons measuring 18:1 in light mode and
 * 1.18:1 in dark. The cause was never the buttons: a background token flips
 * between themes while a hard-coded foreground cannot follow it. Fixing the
 * header fixed the instances and left the mechanism, and on 2026-10-16 QA
 * still had seventeen rules of the same shape -- `.button` itself among them,
 * at 2.67:1 in dark mode. Light mode looked perfect in every one of them,
 * which is why nobody saw it.
 *
 * So the assertion is not about particular selectors. It is that no rule may
 * put a fixed colour on a background that moves.
 */

const AA_NORMAL = 4.5;

function luminance(hex) {
  let h = hex.replace("#", "");
  if (h.length === 3) h = [...h].map((c) => c + c).join("");
  const [r, g, b] = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16) / 255);
  const f = (c) => (c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}

function contrast(a, b) {
  const [x, y] = [luminance(a), luminance(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
}

/*
 * First definition wins for the light table.
 *
 * This is the one subtle part, and getting it wrong makes the whole check
 * report success. A plain map built by iterating every `--token: value` in the
 * file ends up holding the LAST definition of each token, which is the dark
 * override -- so light and dark compare equal, no token looks like it inverts,
 * and the audit finds nothing. That is exactly what happened on the first
 * attempt at this check, and the only reason it was caught is that a known-bad
 * rule was on hand to test it against.
 */
function tokenTable(source, { firstWins }) {
  const table = new Map();
  const re = /--([a-z0-9-]+):\s*(#[0-9a-fA-F]{3,8})\s*(?:!important)?\s*[;}]/g;
  for (const m of source.matchAll(re)) {
    if (firstWins && table.has(m[1])) continue;
    table.set(m[1], m[2]);
  }
  return table;
}

function themeTables(source) {
  const light = tokenTable(source, { firstWins: true });
  // Both dark paths: the media query and the explicit data-theme opt-in.
  const darkSources = [
    ...source.matchAll(/prefers-color-scheme:\s*dark\)\s*\{([\s\S]*?)\n\}/g),
    ...source.matchAll(/:root\[data-theme=["']?dark["']?\]\s*\{([\s\S]*?)\n\}/g),
  ].map((m) => m[1]);
  const dark = new Map(light);
  for (const block of darkSources) {
    for (const [k, v] of tokenTable(block, { firstWins: true })) dark.set(k, v);
  }
  return { light, dark };
}

// Rules carrying a literal colour over a var() background.
function rulesWithFixedForeground(source) {
  const found = [];
  const ruleRe = /([^{}]+)\{([^{}]*)\}/g;
  for (const m of source.matchAll(ruleRe)) {
    const [selector, body] = [m[1].trim(), m[2]];
    if (selector.startsWith("@") || selector.startsWith("/*")) continue;
    const fg = body.match(/(?<![-a-z])color:\s*(white|#[0-9a-fA-F]{3,8})/);
    const bg = body.match(/background(?:-color)?:\s*[^;]*var\(--([a-z0-9-]+)\)/);
    if (!fg || !bg) continue;
    found.push({
      selector: selector.replace(/\s+/g, " ").slice(0, 80),
      foreground: fg[1] === "white" ? "#ffffff" : fg[1],
      backgroundToken: bg[1],
    });
  }
  return found;
}

test("the token table reads light values, not dark overrides", () => {
  const { light, dark } = themeTables(css);
  // The canary. If this ever reads the same in both themes, the parser has
  // regressed to last-wins and every other assertion here is vacuous.
  assert.equal(light.get("color-pitch-deep"), "#0d6848", "light palette");
  assert.equal(dark.get("color-pitch-deep"), "#37d69a", "dark override");
  assert.notEqual(
    light.get("color-signal"),
    dark.get("color-signal"),
    "--color-signal must differ between themes, or this file is parsing one theme twice",
  );
});

test("every paired foreground token is defined in both themes", () => {
  const { light, dark } = themeTables(css);
  const paired = [...light.keys()].filter((k) => k.startsWith("color-on-"));
  assert.ok(paired.length > 0, "expected --color-on-* tokens to exist");
  for (const token of paired) {
    assert.ok(light.get(token), `--${token} missing from the light palette`);
    assert.ok(dark.get(token), `--${token} missing from the dark overrides`);
  }
});

test("no rule puts a fixed colour on a background that inverts", () => {
  const { light, dark } = themeTables(css);
  const failures = [];
  for (const rule of rulesWithFixedForeground(css)) {
    const bgLight = light.get(rule.backgroundToken);
    const bgDark = dark.get(rule.backgroundToken);
    if (!bgLight || !bgDark) continue;
    if (bgLight.toLowerCase() === bgDark.toLowerCase()) continue; // does not invert
    const ratios = [contrast(rule.foreground, bgLight), contrast(rule.foreground, bgDark)];
    if (Math.min(...ratios) < AA_NORMAL) {
      failures.push(
        `${rule.selector}: ${rule.foreground} on --${rule.backgroundToken} ` +
          `= ${ratios[0].toFixed(2)}:1 light, ${ratios[1].toFixed(2)}:1 dark`,
      );
    }
  }
  assert.deepEqual(
    failures,
    [],
    `a fixed foreground cannot follow a background that changes between themes.\n` +
      `Use the paired --color-on-* token for that background instead:\n  ` +
      failures.join("\n  "),
  );
});
