import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";
import { normalizeAccaPage } from "./data/accaApi.ts";

const root = new URL("./", import.meta.url);
const read = (path) => fs.readFileSync(new URL(path, root), "utf8");

test("homepage keeps an ACCA section visible when inventory is empty", () => {
  const source = read("page.tsx");
  assert.match(source, /aria-labelledby=\"featured-accas\"/);
  assert.match(source, /No featured ACCAs are available right now/);
  assert.match(source, /period: \"seven_days\"/);
});

test("homepage promotes ACCAs instead of a duplicate fixtures CTA", () => {
  const source = read("page.tsx");
  assert.match(source, /href=\{localePath\("\/acca-predictions", locale\)\}/);
  assert.match(source, /Browse ACCAs/);
  assert.doesNotMatch(source, /href=\{localePath\("\/matches\/today", locale\)\}/);
  assert.doesNotMatch(source, />View fixtures</);
});

test("homepage prediction ordering considers competition priority before confidence", () => {
  // Was a single global competitionPopularity sort. That ranked by league name
  // via substring, so "Kenyan Premier League" tied with England's and led the
  // page worldwide. rankPredictions keeps the intent -- competition standing
  // outranks confidence -- and adds the reader's own country and continent.
  const source = read("page.tsx");
  assert.match(source, /rankPredictions\(/);
  assert.match(source, /resolveViewerCountry\(/);
  assert.match(source, /leagueCountry: prediction\.country/);
  const css = read("globals.css");
  assert.match(css, /\.home-hero__actions[\s\S]*flex-wrap/);
});

test("homepage ACCAs use bounded two-selection cards and hide invalid odds", () => {
  const source = read("page.tsx");
  const card = read("components/FeaturedAccaCard.tsx");
  assert.match(source, /FeaturedAccaCard/);
  assert.match(card, /summarizeFeaturedAcca/);
  assert.match(card, /summary\.previewLegs\.map/);
  assert.doesNotMatch(card, /bundle\.legs\.map/);
  assert.match(card, /summary\.remainderLabel/);
  assert.match(card, /marketLabel/);
  assert.match(card, /marketSelectionLabel/);
  assert.match(card, /View \{bundle\.legs\.length\}-leg ACCA/);
  assert.match(card, /Number\.isFinite\(bundle\.combinedFairOdds\)/);
  assert.doesNotMatch(card, /combinedFairProbability/);
});

test("homepage actions and header remain responsive without horizontal overflow", () => {
  const css = read("globals.css");
  assert.match(css, /\.home-hero__actions[\s\S]*min-width: 0/);
  assert.match(css, /\.header-utility-links[\s\S]*overflow/);
  /*
   * These two used to assert `.featured-acca-grid` was three across. That
   * class was DEAD -- no component ever rendered it -- so the assertions
   * passed for the entire life of review defect UI-02, during which Home
   * actually used `.ac-grid`, the catalogue's two-column grid, and left a hole
   * bottom-right whenever three cards came back. A test guarding a class
   * nobody uses is worse than no test: it reports the layout is correct.
   *
   * The live grid fills two rows with four cards, and adapts when inventory
   * returns fewer.
   */
  assert.match(css, /\.hm-accas\s*\{[^}]*grid-template-columns: repeat\(2/);
  assert.match(css, /\.hm-accas\[data-count="3"\][\s\S]{0,120}grid-template-columns: repeat\(3/);
  assert.match(css, /\.hm-accas\s*\{[^}]*grid-auto-rows: 1fr/);
  assert.match(css, /\.hm-accas\[data-count="2"\][\s\S]{0,120}grid-template-columns: minmax\(0, 1fr\)/,
    "two cards stack rather than leaving a third cell empty");
  assert.doesNotMatch(css, /\.featured-acca-grid/,
    "the dead grid class must not come back");
  assert.match(read("page.tsx"), /limit: "4"/,
    "Home asks for four featured ACCAs");
  assert.match(css, /\.featured-acca-card[\s\S]*grid-template-rows/);
  assert.match(css, /\.featured-acca-card__legs[\s\S]*grid-template-rows:\s*repeat\(2/);
  assert.match(css, /\.featured-acca-card__leg[\s\S]*color:\s*inherit[\s\S]*text-decoration:\s*none/);
  // Tablet and mobile take full-width stacked cards, per the review.
  assert.match(css, /@media \(max-width: 1180px\)[\s\S]*?\.hm-accas\s*\{[^}]*grid-template-columns: minmax\(0, 1fr\)/);
});

test("catalogue legs stack both teams with crests, on an even row height", () => {
  // Rewritten for canvas V31 (review UI-04/UI-05). This test used to assert the
  // opposite -- one line per leg, a quiet "<i>v</i>", and a 64px row -- because
  // that was the 2026-09-27 refresh's row. The review replaced it: each leg now
  // stacks home over away, each line carrying its own crest and its own
  // ellipsis, and the row grows to 92px (96 at tablet and mobile).
  //
  // Verified against the V31 export rather than inferred: the leg's team
  // container in pages/accas.html is `display: flex; flex-direction: column;
  // gap: 4px; min-width: 0`, with each side a flex row holding the crest.
  //
  // The property worth guarding is unchanged in spirit -- a leg must not wrap
  // unpredictably, and every leg in a card must be the same height, because
  // .ac-grid uses grid-auto-rows: 1fr and one mis-measured card drags the whole
  // catalogue to its height.
  const card = read("acca-predictions/AccaBundleCard.tsx");
  const css = read("globals.css");

  assert.match(card, /<TeamCrest[^>]*logoUrl=\{leg\.homeTeamLogo\}/,
    "the home leg carries its crest");
  assert.match(card, /<TeamCrest[^>]*logoUrl=\{leg\.awayTeamLogo\}/,
    "the away leg carries its crest");
  assert.doesNotMatch(card, /<i>v<\/i>/,
    "stacked teams need no versus mark between them");
  assert.match(css, /\.ac-leg__teams\s*\{[^}]*display:\s*grid/,
    "the two teams stack rather than sharing one line");
  assert.match(css, /\.ac-leg__teamname\s*\{[^}]*text-overflow:\s*ellipsis/,
    "each name truncates on its own budget, not across the pair");
  assert.match(css, /\.ac-leg\s*\{[\s\S]*?min-height:\s*92px/,
    "rows keep an even height so a card's legs form a regular column");
  assert.match(css, /@media \(max-width:\s*900px\)[\s\S]*?\.ac-leg\s*\{[\s\S]*?min-height:\s*96px/,
    "tablet and mobile take the taller 96px leg");

  // MatchupIdentity's own compactness rules still matter to the four surfaces
  // that do use it; see the reuse test below.
  assert.match(css, /\.matchup-identity__versus[\s\S]*font-size:\s*11px/);
  assert.doesNotMatch(css, /\.matchup-identity__versus[\s\S]{0,180}border-radius/);
});

test("ACCA catalogue links to the generator", () => {
  // The generator is the Builder's Generate mode since the 2026-10-02 merge, and
  // `/multi-match-builder` is the URL it canonicalises to. Linking
  // `/acca-generator` from inside the app would point every reader at a page that
  // declares a different canonical -- harmless for them, wasteful for a crawler,
  // and it would keep a URL alive that the merge is trying to consolidate.
  const source = read("acca-predictions/page.tsx");
  assert.match(source, /href="\/multi-match-builder"/);
  assert.match(source, /Generate an ACCA/);
});

test("builder excludes started matches and ranks recommendations before kickoff", () => {
  const source = read("multi-match-builder/BuilderClient.tsx");
  assert.match(source, /filter\(\(candidate\) => !hasBuilderLegStarted/);
  assert.match(source, /item\.isBestBet \? 100/);
});

test("catalogue load-more requests are single-flight", () => {
  const source = read("acca-predictions/AccaExplorer.tsx");
  assert.match(source, /loadInFlight/);
  assert.match(source, /if \(!replace && loadInFlight\.current\) return/);
  assert.match(source, /seenCursors/);
  assert.match(source, /Load more ACCAs/);
  assert.match(source, /result\.nextCursor && result\.nextCursor !== cursor/);
  assert.doesNotMatch(source, /bundles\.length < total/);
});

test("local search controls are available on predictions, ACCAs, and Builder", () => {
  // The guarantee is availability, as the name says: each browse surface can be
  // narrowed without a round trip. This asserted one identical placeholder
  // string across all three, which the refresh changed on the ACCA page to the
  // worked examples its design specifies ("e.g. Girona, Liga MX"). A shared
  // placeholder was a proxy for the control existing, not the point of it.
  for (const file of [
    "components/PredictionExplorer.tsx",
    "acca-predictions/AccaExplorer.tsx",
    "multi-match-builder/BuilderClient.tsx",
  ]) {
    const source = read(file);
    assert.match(source, /value=\{search\}/, `${file} must bind a local search value`);
    assert.match(source, /placeholder=/, `${file}'s search box must say what it takes`);
  }
});

test("prediction, ACCA, and Builder matchup rows reuse MatchupIdentity", () => {
  const component = read("components/MatchupIdentity.tsx");
  assert.match(component, /export function MatchupIdentity/);
  assert.match(component, /matchup-identity__versus/);
  assert.match(component, /<TeamIdentity[\s\S]*<TeamIdentity/);

  // Two surfaces are deliberately absent since the 2026-09-27 refresh: the
  // ACCA catalogue's leg row (AccaBundleCard.tsx) and the today board's
  // fixture row (PredictionExplorer.tsx). Both are dense tabular rows the
  // design draws as plain text -- "Home v Away", no crests -- so there is no
  // matchup lockup in them to share.
  //
  // That leaves MatchupIdentity as the CARD surfaces' component rather than a
  // universal one, which is a coherent split: cards have room for crests and
  // rows do not. What still must not happen anywhere is a fall back to a raw
  // TeamIdentity pair, which is the regression this actually guards.
  for (const file of [
    "components/FeaturedAccaCard.tsx",
    "components/AccaGenerator.tsx",
    "multi-match-builder/BuilderClient.tsx",
  ]) {
    const source = read(file);
    assert.match(source, /import \{ MatchupIdentity \}/);
    assert.match(source, /<MatchupIdentity/);
    assert.doesNotMatch(source, /<TeamIdentity/);
  }
});

test("matchups use equal columns and explorer inputs share filter styling", () => {
  const css = read("globals.css");
  assert.match(css, /\.matchup-identity\s*\{[\s\S]*grid-template-columns:\s*minmax\(0, 1fr\)\s+24px\s+minmax\(0, 1fr\)/);
  assert.doesNotMatch(css, /\.acca-leg__teams \.team-line:last-child\s*\{[^}]*justify-self:\s*end/);
  assert.match(css, /\.prediction-explorer__field input,[\s\S]*\.prediction-explorer__field select/);
  assert.match(css, /\.prediction-explorer__field input::placeholder/);
  assert.match(css, /\.prediction-explorer__field input:focus-visible/);
});

test("global search links prediction and ACCA results to their actionable destinations", () => {
  // The destinations moved into one shared resolver. They used to be written
  // out in search/page.tsx and separately (incompletely) in the autocomplete,
  // which is how prediction rows in the dropdown ended up linking back to the
  // search page instead of opening their fixture.
  const source = read("lib/searchHref.ts");
  assert.match(source, /case "prediction"/);
  assert.match(source, /case "acca"/);
  assert.match(source, /hit\.match_id/);
  assert.match(source, /hit\.key/);

  for (const path of ["search/page.tsx", "components/SearchBox.tsx"]) {
    assert.match(read(path), /searchHitHref/, path);
  }
  assert.match(read("search/page.tsx"), /<BackButton/);
});

test("ACCA generator controls use the shared polished field styling", () => {
  // `.ac-field` is that shared styling, and the 2026-10-02 Builder merge moved
  // these controls onto it from a generator-specific copy -- which is what this
  // test was always asking for: one field style, not one per surface.
  const source = read("components/AccaGenerator.tsx");
  const css = read("globals.css");
  assert.match(source, /className="ac-field"/);
  assert.match(css, /\.ac-field input/);
  assert.match(css, /\.ac-field select/);
});

test("ACCA facets preserve countries, leagues, and markets for filter controls", () => {
  const source = read("data/accaApi.ts");
  assert.match(source, /leagues:/);
});

test("ACCA API normalization drops malformed and duplicate slips", () => {
  const source = read("data/accaApi.ts");
  assert.match(source, /isValidBundle/);
  assert.match(source, /Number\.isFinite\(item\.probability\)/);
  assert.match(source, /new Map\(rows/);
});

test("API fair odds become finite leg probabilities for the web", () => {
  const page = normalizeAccaPage({ items: [{ id: "slip-1", combined_fair_odds: 3.2, legs: [
    { prediction_id: 1, match_id: 2, home_team: "Home", away_team: "Away", league: "League", country: "GB", kickoff_at: "2026-09-02T12:00:00Z", market: "1x2", selection: "home", fair_odds: 2 },
    { prediction_id: 3, match_id: 4, home_team: "Other", away_team: "Visitor", league: "League", country: "GB", kickoff_at: "2026-09-02T15:00:00Z", market: "btts", selection: "yes", fair_odds: 1.6 },
  ] }] });
  assert.equal(page.bundles[0].legs[0].probability, 0.5);
  assert.equal(page.bundles[0].combinedFairProbability, 0.3125);
});

test("ACCA surfaces do not render infinite odds or zero probability claims", () => {
  const explorer = read("acca-predictions/AccaExplorer.tsx");
  const home = read("page.tsx");
  assert.doesNotMatch(explorer, /Fair probability/);
  assert.doesNotMatch(home, /Fair probability/);
  assert.doesNotMatch(explorer, /1 \/ leg\.probability/);
});

test("catalogue keeps bundle labels without repeating removed card copy", () => {
  const source = read("acca-predictions/AccaExplorer.tsx");
  assert.match(source, /bundle\.label/);
  assert.doesNotMatch(source, /System-generated ACCA/);
  assert.doesNotMatch(source, /bundle\.description/);
});

test("market landing pages query canonical inventory tabs", () => {
  const source = read("acca-predictions/accaMarkets.ts");
  assert.match(source, /slug: "corners-accas"[\s\S]*?marketKeys: \["total_corners"\]/);
  assert.match(source, /slug: "cards-accas"[\s\S]*?marketKeys: \["total_cards"\]/);
});

test("selection-specific ACCA pages preserve their selection filter", () => {
  const source = read("acca-predictions/[marketSlug]/page.tsx");
  assert.match(source, /selection: definition\.selection/);
  assert.match(source, /initialSelection=\{definition\.selection\}/);
  assert.match(read("acca-predictions/AccaExplorer.tsx"), /query\.set\("selection"/);
});

test("generator accepts integer leg counts from 2 through 20 and renders a result", () => {
  const source = read("components/AccaGenerator.tsx");
  assert.match(source, /type="number"/);
  // The bounds are named constants now, because the stepper added in the Builder
  // merge has to clamp to the same numbers the input accepts -- a stepper that
  // stops at 20 beside an input taking 50 is a validation error a reader can only
  // find by submitting.
  assert.match(source, /const MIN_GENERATOR_LEGS = 2;/);
  assert.match(source, /const MAX_GENERATOR_LEGS = 20;/);
  assert.match(source, /min=\{MIN_GENERATOR_LEGS\}/);
  assert.match(source, /max=\{MAX_GENERATOR_LEGS\}/);
  assert.match(source, /Math\.max\(MIN_GENERATOR_LEGS/, "the stepper must clamp to the same lower bound");
  assert.match(source, /Math\.min\(MAX_GENERATOR_LEGS/, "the stepper must clamp to the same upper bound");
  // The design labels the result "Generated for you"; "Generator result" named
  // the machine rather than the thing it made.
  assert.match(source, /Generated for you/);
});

test("generator mixed markets choice requests the mixed inventory", () => {
  const source = read("components/AccaGenerator.tsx");
  assert.match(source, /\[market, setMarket\] = useState\("mixed"\)/);
  assert.match(source, /<option value="mixed">Mixed markets<\/option>/);
});

test("the BTTS chip names its market instead of its selection", () => {
  const markets = read("acca-predictions/accaMarkets.ts");
  // "Yes ACCAs" read as a selection with no market attached, and BTTS also has
  // a No side, so the chip and heading must say BTTS.
  assert.match(markets, /slug: "btts-accas"[\s\S]*?shortLabel: "BTTS"/);
  assert.match(markets, /if \(definition\.shortLabel\) return formatTerm\(definition\.shortLabel, locale\)/);
  assert.match(read("lib/markets.ts"), /"BTTS": "Ambos marcan"/);
});

test("the generator composes from the vetted pool when inventory has no match", () => {
  const generator = read("components/AccaGenerator.tsx");
  assert.match(generator, /generate: "1"/);
  assert.match(generator, /bundle\.origin === "on_demand"/);
  // The empty state stays honest about a bounded search: it does not claim a
  // mathematical impossibility when no result was found inside the budget.
  assert.match(generator, /No ACCA found for these filters/);
  assert.match(read("api/accas/route.ts"), /"generate"/);
  assert.match(read("data/accaApi.ts"), /origin: text\(raw\.origin\)/);
  assert.doesNotMatch(read("acca-generator/page.tsx"), /never composes/);
});

test("mobile web puts account in the fifth bottom-nav slot without crowding the header", () => {
  const header = read("components/PublicHeader.tsx");
  assert.match(header, /className="account-link"/);
  assert.doesNotMatch(header, /account-link__icon|name="account"/);
  const mobileNav = read("components/MobileBottomNav.tsx");
  assert.doesNotMatch(mobileNav, /nav\.search|icon: "search"/);
  assert.match(mobileNav, /authenticated \? "\/account" : "\/login"/);
  assert.match(mobileNav, /icon: "account"/);
  const css = read("globals.css");
  assert.match(css, /@media \(max-width: 767px\)[\s\S]*?\.account-link \{\s*display: none;/);
  assert.match(read("layout.tsx"), /href=\{localePath\("\/login", locale\)\}/);
});

test("browse surfaces compose when a filter corner has no stored slip", () => {
  const explorer = read("acca-predictions/AccaExplorer.tsx");
  // First page only: a cursor pages through stored inventory.
  assert.match(explorer, /if \(replace \|\| !cursor\) query\.set\("generate", "1"\)/);
  // The label moved to the card component in the 2026-09-27 refresh; the
  // guarantee is that a live-composed bundle still says so on its face.
  assert.match(read("acca-predictions/AccaBundleCard.tsx"), /bundle\.origin === "on_demand"/);
  assert.match(read("acca-predictions/page.tsx"), /generate: "1"/);
  assert.match(read("acca-predictions/[marketSlug]/page.tsx"), /generate: "1"/);
});
