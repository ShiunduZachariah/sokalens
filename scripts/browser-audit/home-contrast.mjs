import { chromium } from "playwright";
import { AxeBuilder } from "@axe-core/playwright";
const browser = await chromium.launch();
const context = await browser.newContext();
const page = await context.newPage();
await page.goto("http://localhost:3000/", { waitUntil: "domcontentloaded", timeout: 30000 });
await page.waitForTimeout(2500);
const r = await new AxeBuilder({ page }).withRules(["color-contrast"]).analyze();
const v = r.violations[0];
console.log("nodes: " + (v ? v.nodes.length : 0));
for (const node of (v ? v.nodes : [])) {
  console.log("--- " + (node.target || []).join(", "));
  console.log("html: " + (node.html || "").slice(0, 120));
  console.log("why: " + (node.failureSummary || "").slice(0, 160));
}
await browser.close();
