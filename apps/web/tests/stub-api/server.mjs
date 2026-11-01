import { createServer } from "node:http";
import { dayOffsetFor, generatedSlip, listSlips, marketKeys, predictionsForDate, slipById, slipShapes } from "./fixtures.mjs";

/**
 * A stand-in for the prediction API, for end-to-end runs.
 *
 * ## Why this exists rather than being deleted after the run
 *
 * The Ephemeral Tooling Rule's test is whether a human will deliberately run it
 * again, and the answer here is yes, every time anyone runs the e2e suite. The
 * web app is useless without an API: every page falls to an empty state, so a
 * browser test against a bare `next start` proves that the shells render and
 * nothing else. With this, a test can open an ACCA that has a lost leg and
 * assert what the page says about it.
 *
 * It is NOT a mock of the API's behaviour and must not be used to decide
 * whether the backend is correct. It serves the wire SHAPES the web layer
 * parses, taken from the normalizers in `app/data/*`, so a journey test can
 * exercise the UI. The Go test suite is what checks the API.
 *
 * Run it with `npm run test:e2e`, which starts it through Playwright's
 * `webServer` so it is stopped when the run ends.
 *
 * ## What it answers
 *
 * Every path the web layer reads, listed in one place at the bottom of this
 * file. A path it does not know about returns 404 with a JSON error, in the
 * API's own error shape -- not a hang and not an HTML page, either of which
 * would present as a bug in the page under test rather than a gap here.
 */

const PORT = Number(process.env.STUB_API_PORT ?? 8099);

const json = (response, status, body) => {
  const payload = JSON.stringify(body);
  response.writeHead(status, {
    "Content-Type": "application/json",
    "Content-Length": Buffer.byteLength(payload),
    // The web layer sets its own cache policy per call; the stub never asks a
    // proxy to hold anything, so a test that changes a fixture sees it.
    "Cache-Control": "no-store",
  });
  response.end(payload);
};

const notFound = (response, code = "not_found") =>
  json(response, 404, { error: { code, message: "Not found." } });

function predictionsResponse(url) {
  const offset = dayOffsetFor(url.searchParams.get("date"));
  if (offset === null) return { total: 0, page: 1, limit: 0, predictions: [], provider_predictions: [] };
  const market = url.searchParams.get("market") ?? "1x2";
  const limit = Math.min(100, Math.max(1, Number(url.searchParams.get("limit") ?? 40)));
  const page = Math.max(1, Number(url.searchParams.get("page") ?? 1));
  const rows = predictionsForDate(offset, market);
  return {
    total: rows.length,
    page,
    limit,
    predictions: rows.slice((page - 1) * limit, page * limit),
    // The merge path reads this key; an empty list exercises it without a
    // second set of fixtures that would have to agree with the first.
    provider_predictions: [],
  };
}

function accaListResponse(url) {
  const items = listSlips();
  const legs = Number(url.searchParams.get("legs") ?? 0);
  const filtered = Number.isFinite(legs) && legs > 0
    ? items.filter((item) => item.leg_count === legs)
    : items;
  // `generate=1` is how the generator asks for a slip to be composed when the
  // inventory holds none for these filters. It is composed to the requested leg
  // count: answering with a catalogue slip of some other size would make the
  // generator look broken for every count but the one the catalogue holds.
  const generated = url.searchParams.get("generate") === "1" && filtered.length === 0
    ? [generatedSlip(legs, Number(url.searchParams.get("min_fair_odds")), Number(url.searchParams.get("max_fair_odds")))]
    : [];
  const result = filtered.length > 0 ? filtered : generated;
  return {
    items: result,
    limit: Math.max(1, Number(url.searchParams.get("limit") ?? 10)),
    total: result.length,
    facets: {
      total: result.length,
      markets: marketKeys,
      countries: ["GB", "ES", "IT"],
      leagues: ["Premier League", "La Liga", "Serie A"],
      leg_counts: [...new Set(items.map((item) => item.leg_count))].sort(),
      league_countries: { GB: ["Premier League"], ES: ["La Liga"], IT: ["Serie A"] },
    },
  };
}

function matchesTodayResponse(url) {
  const offset = dayOffsetFor(url.searchParams.get("date"));
  if (offset === null) return { matches: [], total: 0 };
  const rows = predictionsForDate(offset, "1x2").map((row) => ({
    id: row.match_id,
    kickoff_at: row.kickoff_at,
    status: row.match_status,
    home_team: { id: row.match_id * 2, name: row.home_team, logo_url: "" },
    away_team: { id: row.match_id * 2 + 1, name: row.away_team, logo_url: "" },
    league: { id: 1, name: row.league_name, country_code: row.country },
    home_score: null,
    away_score: null,
  }));
  return { matches: rows, total: rows.length };
}

const server = createServer((request, response) => {
  const url = new URL(request.url ?? "/", `http://localhost:${PORT}`);
  const { pathname } = url;

  if (pathname === "/healthz") return json(response, 200, { status: "ok" });

  if (pathname === "/v1/predictions/today") return json(response, 200, predictionsResponse(url));
  if (pathname === "/v1/predictions/results") return json(response, 200, { items: [], total: 0, limit: 20 });
  if (pathname === "/v1/predictions/hit-rate") {
    return json(response, 200, { overall: { settled: 0, correct: 0, hit_rate: 0 }, markets: [] });
  }

  if (pathname === "/v1/accas" || pathname === "/v1/accas/featured") {
    return json(response, 200, accaListResponse(url));
  }
  if (pathname.startsWith("/v1/accas/")) {
    const id = pathname.slice("/v1/accas/".length).replace(/\/$/, "");
    if (id.includes("/")) return notFound(response, "acca_not_found");
    const found = slipById(id);
    return found ? json(response, 200, found) : notFound(response, "acca_not_found");
  }

  if (pathname === "/v1/matches/today") return json(response, 200, matchesTodayResponse(url));
  if (/^\/v1\/matches\/\d+$/.test(pathname)) {
    const id = Number(pathname.split("/").pop());
    const rows = matchesTodayResponse(new URL(`http://x/?date=${new Date().toISOString().slice(0, 10)}`)).matches;
    const match = rows.find((item) => item.id === id);
    return match ? json(response, 200, { ...match, predictions: [], events: [] }) : notFound(response, "match_not_found");
  }

  // In-play is entitlement-gated upstream, and an unentitled reader gets a 404
  // for the whole prefix. That is the honest default for an anonymous browser
  // test, and it exercises the locked state rather than a state no visitor has.
  if (pathname.startsWith("/v1/inplay/")) return notFound(response, "not_entitled");

  // Anonymous. The account proxy turns this into the signed-out state, which is
  // what every journey here starts from.
  if (pathname.startsWith("/v1/me/")) return json(response, 401, { error: { code: "unauthenticated", message: "Sign in." } });

  if (pathname === "/v1/search/suggest") return json(response, 200, { suggestions: [] });
  if (pathname.startsWith("/v1/ads/")) return json(response, 200, { placements: [] });

  return notFound(response);
});

server.listen(PORT, () => {
  process.stdout.write(`stub prediction API listening on http://127.0.0.1:${PORT}\n`);
  process.stdout.write(`slip ids: ${slipShapes.map((shape) => shape.id).join(", ")}\n`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
