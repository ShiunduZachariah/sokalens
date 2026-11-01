import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";

const BASE = "http://localhost:3000";
const routes = ["/", "/predictions/today", "/matches/today", "/best-bets", "/leagues", "/search?q=arsenal", "/responsible-gambling"];

const browser = await chromium.launch();
const context = await browser.newContext();
const page = await context.newPage();
const summary = [];

for (const route of routes) {
  const url = BASE + route;
  let ok = true;
  try { await page.goto(url, { waitUntil: "domcontentloaded", timeout: 30000 }); await page.waitForTimeout(2500); }
  catch (e) { ok = false; console.log("LOAD FAIL " + route + ": " + e.message); }
  if (!ok) { summary.push({ route, error: "load failed" }); continue; }

  const title = await page.title();
  const h1 = await page.locator("h1").count();
  let axe;
  try {
    const r = await new AxeBuilder({ page }).withTags(["wcag2a", "wcag2aa"]).analyze();
    axe = {
      total: r.violations.length,
      serious: r.violations.filter((v) => v.impact === "serious" || v.impact === "critical").map((v) => ({ id: v.id, impact: v.impact, nodes: v.nodes.length })),
      contrast: r.violations.filter((v) => v.id === "color-contrast").length,
    };
  } catch (e) { axe = { error: e.message }; }

  let focusMoves = false;
  try {
    for (let i = 0; i < 8; i++) await page.keyboard.press("Tab");
    const tag = await page.evaluate(() => (document.activeElement ? document.activeElement.tagName : "NONE"));
    focusMoves = tag !== "BODY" && tag !== "NONE";
  } catch (e) {}

  summary.push({ route, title: title.slice(0, 50), h1, axe, keyboardFocusMoves: focusMoves });
  const s = axe && axe.serious ? axe.serious.length : -1;
  console.log(route + " | h1=" + h1 + " | axeSerious=" + s + " | contrast=" + (axe && typeof axe.contrast === "number" ? axe.contrast : "?") + " | focus=" + focusMoves);
}

console.log("\n===== SUMMARY =====");
console.log(JSON.stringify(summary, null, 2));
await browser.close();
