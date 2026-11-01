import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";

const browser = await chromium.launch();
const context = await browser.newContext();
const page = await context.newPage();
await page.goto("http://localhost:3000/matches/today", { waitUntil: "domcontentloaded", timeout: 30000 });
await page.waitForTimeout(2500);
const r = await new AxeBuilder({ page }).withRules(["color-contrast"]).analyze();
const v = r.violations[0];
console.log("contrast nodes: " + (v && v.nodes ? v.nodes.length : 0));
const seen = new Set();
for (const node of (v ? v.nodes : [])) {
  const sel = (node.target || []).join(", ");
  if (seen.has(sel)) continue;
  seen.add(sel);
  console.log("\n--- " + sel);
  console.log("html: " + (node.html || "").slice(0, 140));
  console.log("why: " + (node.failureSummary || "").slice(0, 180));
}
await browser.close();
