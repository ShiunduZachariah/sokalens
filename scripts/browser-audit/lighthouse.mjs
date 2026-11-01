import lighthouse from "lighthouse";
import * as chromeLauncher from "chrome-launcher";
import { mkdirSync } from "node:fs";
import { join } from "node:path";

async function runOnce(url, preset, i) {
  const profile = join(process.cwd(), ".lh-profile-" + i);
  mkdirSync(profile, { recursive: true });
  const chrome = await chromeLauncher.launch({ chromeFlags: ["--headless", "--no-sandbox", "--disable-gpu"], userDataDir: profile });
  try {
    const result = await lighthouse(url, { port: chrome.port, preset, onlyCategories: ["performance", "accessibility", "best-practices", "seo"] }, null);
    const lhr = result.lhr;
    const scores = {};
    for (const [k, v] of Object.entries(lhr.categories)) scores[k] = Math.round(v.score * 100);
    const lcp = lhr.audits["largest-contentful-paint"] ? Math.round(lhr.audits["largest-contentful-paint"].numericValue || 0) : null;
    const cls = lhr.audits["cumulative-layout-shift"] ? (lhr.audits["cumulative-layout-shift"].numericValue || 0).toFixed(3) : null;
    const tbt = lhr.audits["total-blocking-time"] ? Math.round(lhr.audits["total-blocking-time"].numericValue || 0) : null;
    return { scores, lcpMs: lcp, cls, tbtMs: tbt };
  } finally { await chrome.kill(); }
}

const BASE = "http://localhost:3000";
const jobs = [["/", "desktop"], ["/predictions/today", "mobile"], ["/leagues", "mobile"], ["/best-bets", "mobile"]];
let i = 0;
for (const [route, preset] of jobs) {
  i++;
  try {
    const r = await runOnce(BASE + route, preset, i);
    console.log(preset + " " + route + " => perf=" + r.scores.performance + " a11y=" + r.scores.accessibility + " bp=" + r.scores["best-practices"] + " seo=" + r.scores.seo + " LCP=" + r.lcpMs + "ms CLS=" + r.cls + " TBT=" + r.tbtMs + "ms");
  } catch (e) {
    console.log(preset + " " + route + " => ERROR " + e.message);
  }
}
