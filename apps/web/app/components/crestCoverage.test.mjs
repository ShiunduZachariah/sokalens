import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

/**
 * Phase D of the 2026-10-15 V31 review: a crest beside every team name.
 *
 * Acceptance criterion 1 is "no team name appears anywhere without a crest of
 * the size in 2.1, and a missing logo shows the initials shield, never a broken
 * image". The shield half is `TeamCrest`'s own business and is asserted below
 * once; the rest of this file is per surface, because the defect was never that
 * the primitive was wrong -- it was that eight surfaces did not call it.
 *
 * Each size assertion carries the arithmetic it came from. The V31 export sets
 * every crest at `width: 1.4em` of its surface's own type size, so the size is
 * derived, not chosen: a 15px row is 21 and takes the review's 20, an 18px card
 * head is 25.2 and takes 24, a 32px hero is 44.8 and takes 44. Checking the
 * number here is what stops the next change picking a size that looks fine in
 * isolation and wrong beside the text it sits in.
 */

const root = new URL("../", import.meta.url);
const read = (path) => fs.readFileSync(new URL(path, root), "utf8");

test("TeamCrest is the only team crest, and a missing logo is a shield not a gap", () => {
  const crest = read("components/TeamCrest.tsx");

  // The box is fixed before the image loads, which is what stops a row
  // reflowing when a provider logo arrives late or never.
  assert.match(crest, /width=\{size\}/, "the box sets its own width");
  assert.match(crest, /height=\{size\}/, "the box sets its own height");
  assert.match(crest, /sl-teamcrest--shield/, "there is a shield path at all");
  assert.match(crest, /hasImage\(logoUrl\)/,
    "an unusable logo takes the shield rather than rendering an empty img");

  const css = read("globals.css");
  for (const size of [16, 20, 24, 32, 44]) {
    assert.match(css, new RegExp(`\\.sl-teamcrest--${size}\\s*\\{[^}]*width: ${size}px`),
      `the review's ${size}px size exists`);
  }
  // Same box for the shield as for the logo: the fallback must not be a
  // different size, or a list of mixed rows would not line up.
  assert.match(css, /\.sl-teamcrest--logo\s*\{[^}]*object-fit: contain/,
    "provider art arrives at every aspect ratio and must not be cropped");
});

test("the shared team block gives each line its own truncation budget", () => {
  // This is the whole of UI-05. When the ellipsis sat on the joined
  // "Home v Away" string, two long names lost BOTH halves to one ellipsis and
  // the away side always lost first -- the reported case was
  // "GAP Connah S Quay FC v Penyb...".
  const css = read("globals.css");
  assert.match(css, /\.sl-teams\s*\{[^}]*min-width: 0/,
    "the container may shrink below its content");
  assert.match(css, /\.sl-team\s*\{[^}]*min-width: 0/,
    "and so may each line, independently");
  assert.match(css, /\.sl-teamname\s*\{[^}]*text-overflow: ellipsis/,
    "the ellipsis is on the NAME, not on the pair");
  assert.match(css, /\.sl-teams--inline\s*\{[^}]*display: flex/,
    "the inline variant exists for rows too short for two lines");
});

test("one component draws every home-v-away pair", () => {
  // Three overlapping primitives before task A3, and a fourth composition
  // beside TeamIdentity and MatchupIdentity would have been the same mistake:
  // the V31 export draws this pair three ways, so the component takes three
  // layouts rather than being copied three times.
  const matchup = read("components/MatchupIdentity.tsx");
  for (const layout of ["columns", "stacked", "inline"]) {
    assert.match(matchup, new RegExp(`"${layout}"`), `the ${layout} layout exists`);
  }
  assert.match(matchup, /layout = "columns"/,
    "columns stays the default so the pre-existing call sites keep their shape");
});

test("Best Bets crests the top pick, the ranked rows and the locked rows", () => {
  const explorer = read("components/BestBetExplorer.tsx");
  const locked = read("components/BestBetsLocked.tsx");
  const css = read("globals.css");

  // The payload: `/v1/best-bets` serialises through predictionSummaryResponse,
  // and task F1 added the logo to the provider fallback rows, so engine and
  // provider rows in the same ranking both carry one.
  assert.match(read("best-bets/page.tsx"), /homeTeamLogo: item\.homeTeamLogo/,
    "the page passes the crest through to the row");

  assert.match(explorer, /homeTeamLogo\?: string/, "BestBetItem carries the crests");
  // 1.4em of the hero's 32px headline is 44.8.
  assert.match(explorer, /className="bb-hero__match"[\s\S]{0,220}size=\{44\}/,
    "the top pick takes the 44px crest");
  // 1.4em of a ranked row's 15px name is 21.
  assert.match(explorer, /className="bb-row__teams"[\s\S]{0,220}size=\{20\}/,
    "a ranked row takes the 20px crest");
  assert.match(locked, /className="bb-row__teams"[\s\S]{0,220}size=\{20\}/,
    "and so does a blurred row -- it is the same row behind a filter");

  // The blurred teaser and the row it becomes on sign-in must be the same
  // height, or the list jumps at the moment the gate lifts.
  assert.match(css, /\.bb-rows a\s*\{[\s\S]*?min-height: 76px/);
  assert.match(css, /\.bb-locked__teaser \.bb-rows > li > span\s*\{[\s\S]*?min-height: 76px/);
});

test("the in-play board crests live, starting-soon and finished rows", () => {
  const board = read("components/InPlayBoard.tsx");
  const page = read("in-play/page.tsx");

  // in-play.html draws each live side at 15px with the crest inside the name
  // cell, so the two scores stay in one column a reader can compare.
  assert.match(board, /logoUrl=\{match\.homeTeamLogo\}[\s\S]{0,60}size=\{20\}/);
  assert.match(board, /logoUrl=\{match\.awayTeamLogo\}[\s\S]{0,60}size=\{20\}/);
  // The side's class is composed at runtime (`ip-side` plus a lead/trail
  // modifier), so what is asserted is that `sl-team` is appended to it: the
  // side is a crest-plus-name row now, not a bare text cell.
  assert.match(board, /\$\{sideClass\("home"\)\} sl-team/);
  assert.match(board, /\$\{sideClass\("away"\)\} sl-team/);
  assert.match(board, /<span className="sl-teamname">\{match\.homeTeam\}<\/span>/,
    "and the name keeps its own ellipsis so the crest is never the part clipped");

  // Both of these read "Home v Away" on one line before Phase D.
  const soonBlocks = page.match(/className="ip-soon__teams"[\s\S]{0,260}?size=\{20\}/g) ?? [];
  assert.equal(soonBlocks.length, 2, "starting-soon and finished rows both crest their teams");
  assert.doesNotMatch(page, /\{match\.homeTeam\} <i>v<\/i> \{match\.awayTeam\}/,
    "neither row joins the two names into one truncating string any more");
});

test("the Builder crests its match cards and every ticket leg, inline", () => {
  const builder = read("multi-match-builder/BuilderClient.tsx");
  const state = read("multi-match-builder/builder-state.ts");

  // builder.html's card head is 18px: 1.4em is 25.2.
  assert.match(builder, /className="bd-match__teams"[\s\S]{0,240}size=\{24\}/);
  assert.match(builder, /className="bd-match__teams"[\s\S]{0,240}layout="stacked"/,
    "the match card stacks, as the artboard draws it");

  // The ticket leg is INLINE, which is the one place the artboard disagrees
  // with the stacked treatment everywhere else: the leg's subject is the
  // selection, and the ticket is a narrow sticky rail.
  assert.match(builder, /className="bd-leg__teams"[\s\S]{0,260}layout="inline"/,
    "the ticket leg is inline, per review 3 and builder.html");
  // 1.4em of the leg's 13px fixture line is 18.2.
  assert.match(builder, /className="bd-leg__teams"[\s\S]{0,260}size=\{16\}/);

  // The ticket persists to localStorage, so a leg stored before these fields
  // existed must still load -- it falls back to the joined title.
  assert.match(state, /homeTeam\?: string/, "the leg carries the two sides");
  assert.match(state, /typeof candidate\.homeTeam === "string"/,
    "and reads them back defensively, so an older ticket is not discarded");
  assert.match(builder, /leg\.homeTeam && leg\.awayTeam \?/,
    "a leg without them renders the title rather than half a pair");
});

test("fixture and result rows crest both teams, and no team uses the competition crest", () => {
  const rows = read("components/FixtureRows.tsx");

  // `Crest` is a CIRCLE and is for competitions only since task A3: a league
  // badge is a circle, a club is a shield, and this file used to draw clubs
  // with the circle.
  assert.doesNotMatch(rows, /<Crest\b/, "no team is drawn with the competition crest");
  assert.doesNotMatch(rows, /from "\.\/Crest"/, "and the import is gone with it");

  // The league hub and the team page share FixtureGroups, so both get this.
  assert.match(rows, /className="lg-fixture__teams"[\s\S]{0,240}layout="stacked"/);
  assert.match(rows, /className="lg-fixture__teams"[\s\S]{0,240}size=\{20\}/);

  // Result rows stay on ONE line: the score is the row's subject and it belongs
  // between the two names, which stacking would break. Checked against
  // league-premier-league.html and team.html, not inferred from the fixture row
  // above it.
  assert.match(rows, /lg-result__home sl-team/);
  assert.match(rows, /lg-result__away sl-team/);
  assert.match(rows, /logoUrl=\{match\.home_team_logo\}[\s\S]{0,60}size=\{20\}/);
  assert.match(rows, /logoUrl=\{match\.away_team_logo\}[\s\S]{0,60}size=\{20\}/);
});

test("the two surfaces outside the review's table carry crests too", () => {
  // UI-04 is "every team name has its crest, everywhere", and these were the
  // last two team displays in the public app with the data to hand and no
  // crest.
  //
  // The match page's related-fixture cards: review 3 says "the match header
  // already had crests; fixture lists now have them too". 1.4em of the card's
  // 17px name is 23.8.
  const detail = read("components/MatchDetail.tsx");
  assert.match(detail, /mp-fixture__team sl-team[\s\S]{0,160}size=\{24\}/);

  // /predictions/tomorrow's shortlist. Inline on purpose: it has no V31
  // artboard, so it gains the crest without being relaid out.
  const toppick = read("components/TopPickCard.tsx");
  assert.match(toppick, /className="td-toppick__teams"[\s\S]{0,240}layout="inline"/);
  assert.match(toppick, /className="td-toppick__teams"[\s\S]{0,240}size=\{20\}/);
  assert.match(read("predictions/tomorrow/page.tsx"), /homeTeamLogo: prediction\.homeTeamLogo/);
});

test("the home page crests the strongest signal and every pick card", () => {
  const home = read("page.tsx");
  const card = read("components/home/HomePredictionCard.tsx");
  const css = read("globals.css");

  // 1.4em of the signal card's 30px name is 42; it steps to 32 below 900px
  // where that name is 24px, in CSS because the component cannot see the
  // breakpoint.
  assert.match(home, /hm-signal__team sl-team[\s\S]{0,160}size=\{44\}/);
  assert.match(css, /@media \(max-width: 900px\)[\s\S]*?\.hm-signal__team \.sl-teamcrest\s*\{[^}]*width: 32px/);

  // 1.4em of a pick card's 18px name is 25.2.
  assert.match(card, /hm-card__team sl-team[\s\S]{0,160}size=\{24\}/);
  assert.match(card, /awayLogo: string/, "the card's type carries the crests");
  assert.match(home, /homeLogo: prediction\.homeTeamLogo/,
    "and the feed mapper fills them from the payload that already had them");
});

test("the performance page crests its result rows, stacked", () => {
  const results = read("results/page.tsx");
  const mapper = read("data/predictionResults.ts");

  /*
   * The last section 3 surface to get crests, and the one that needed the wire
   * widening: /v1/predictions/results served a pre-joined "Home vs Away" title
   * and nothing else, so there was no name to hash a fallback shield from.
   * Splitting that string back apart would have been recovering structure from
   * a value that had already lost it -- a shield on every row and a real crest
   * on none -- so the serialiser now carries the two sides beside the title.
   *
   * Stacked, not inline: performance.html draws its team container as
   * `flex-direction: column; gap: 4px`, and unlike the league and team result
   * rows there is no score chip between the names forcing them onto one line.
   * Crest 20 = 1.4em of the row's 15px name.
   */
  assert.match(results, /layout="stacked"/, "performance rows stack both teams");
  assert.match(results, /size=\{20\}/, "1.4em of the row's 15px name");
  assert.match(results, /match\.homeTeam && match\.awayTeam \?/,
    "a payload without the two sides falls back to the joined title rather than rendering half a pair");
  assert.match(mapper, /homeTeamLogo: String\(row\.home_team_logo/,
    "the mapper reads the crests the serialiser now sends");
});

test("a league-hub fixture with no pick yet says so, in the design's words", () => {
  /*
   * Task B4, and the only reason it is cheap: `prediction_state` has been on
   * the wire all along -- matchSummaryResponse emits it for every match
   * summary, including the league hub's upcoming_matches -- and CatalogMatch
   * was the reason nothing could read it. Before this, the LeagueEPL artboard's
   * entire purpose (fixtures published, picks not yet) had no counterpart.
   *
   * Copy taken verbatim from league-premier-league.html, not paraphrased from
   * the handoff.
   */
  const rows = read("components/FixtureRows.tsx");
  const catalog = read("data/catalog.ts");
  const css = read("globals.css");

  assert.match(catalog, /prediction_state\?: "published" \| "pending"/,
    "the type has to declare it before anything can read it");
  assert.match(rows, /match\.prediction_state === "pending" \?/);
  assert.match(rows, /Pick publishes closer to kick-off/);

  // Only an explicit "pending" gets the copy: an absent prediction_state means
  // the API expressed no opinion, which is a different answer and has no copy.
  assert.doesNotMatch(rows, /prediction_state !== "published"/,
    "absent must not be treated as pending");

  // Heavier than the status it replaces, per the artboard's pick column.
  assert.match(css, /\.lg-fixture__pending\s*\{[^}]*font-weight: 700/);

  // It must NOT drop with the status at mobile: the mobile artboard keeps it,
  // on a second line indented past the time column.
  assert.match(css, /@media \(max-width: 640px\)[\s\S]*?\.lg-fixture__pending\s*\{[^}]*grid-column: 2 \/ -1/);
});
