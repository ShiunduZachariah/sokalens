import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import test from "node:test";

const routeRoot = new URL("../app/", import.meta.url);

async function source(path) {
  return readFile(new URL(path, routeRoot), "utf8");
}

test("free account routes use the live account components", async () => {
  const localizedMetadata = await source("i18n/metadata.ts");
  const expectations = new Map([
    ["login/page.tsx", "<AuthForm mode=\"login\""],
    ["register/page.tsx", "<AuthForm mode=\"register\""],
    ["verify-email/page.tsx", "<AuthForm mode=\"verify\""],
    ["account/page.tsx", "<AccountDashboard />"],
    ["favorites/page.tsx", "<FavoritesList />"],
    ["forgot-password/page.tsx", "<AuthForm mode=\"forgot\""],
    ["odds-preference/page.tsx", "<OddsPreferenceForm"],
    ["reset-password/page.tsx", "<AuthForm mode=\"reset\""],
  ]);

  for (const [path, marker] of expectations) {
    const contents = await source(path);
    if (contents.includes("localizedAuthMetadata")) assert.match(localizedMetadata, /robots:\s*\{\s*index:\s*false/);
    else assert.match(contents, /robots:\s*\{\s*index:\s*false/);
    assert.ok(contents.includes(marker), `${path} should include ${marker}`);
    assert.doesNotMatch(contents, /<ComingSoon/);
  }
});

test("odds preference is required, server-persisted, and shared globally", async () => {
  const context = await source("components/account/AccountContext.tsx");
  const form = await source("components/account/OddsPreferenceForm.tsx");
  const control = await source("components/OddsFormatControl.tsx");
  const strip = await source("components/OddsStrip.tsx");
  assert.match(context, /oddsFormatSelected/);
  assert.match(context, /accountApiRequest<unknown>\("\/v1\/me"\)/);
  assert.match(context, /const destination = `\/odds-preference/);
  assert.match(context, /router\.replace\(locale \? localePath\(destination, locale\) : destination\)/);
  assert.match(form, /"\/v1\/me\/profile"/);
  assert.match(form, /writeOddsFormat\(format\)/);
  assert.match(control, /odds_format:\s*next/);
  assert.match(strip, /formatOdds\(Number\(price\.value\), oddsFormat\)/);
});

test("mobile builder slip stays beside the brand and has a readable label", async () => {
  const slip = await source("components/BetSlip.tsx");
  const css = await source("globals.css");
  assert.match(slip, /bet-slip-toggle__label">Slip/);
  assert.match(css, /\.header-actions \{ grid-column: 2; grid-row: 1;/);
  assert.match(css, /\.bet-slip-toggle__label \{ display: inline;/);
});

test("today fixtures use bounded server pagination", async () => {
  const page = await source("matches/today/page.tsx");
  assert.match(page, /fetchMatches/);
  assert.match(page, /fetchMatchCount/);
  assert.match(page, /const pageSize = 20/);
  assert.match(page, /Next fixtures/);
  assert.doesNotMatch(page, /fetchAllMatches/);
});

test("published prediction feeds use a one-minute shared cache", async () => {
  const feed = await source("data/livePredictions.ts");
  assert.match(feed, /refresh stampede/);
  assert.match(feed, /revalidate: 60/);
});

test("registration includes country and honeypot fields", async () => {
  const contents = await source("components/account/AuthForm.tsx");
  const countries = await source("components/account/countryOptions.ts");
  const account = await source("components/account/AccountDashboard.tsx");
  const turnstile = await source("components/account/TurnstileWidget.tsx");
  const accountProxy = await source("api/account/[...path]/route.ts");
  assert.match(contents, /name="country_code"/);
  assert.match(contents, /<select[\s\S]*name="country_code"[\s\S]*required/);
  assert.match(contents, /Select your country/);
  assert.match(contents, /countryOptions\.map/);
  assert.doesNotMatch(contents, /Enter the two-letter code|pattern="\[A-Za-z\]\{2\}"/);
  assert.match(countries, /Intl\.DisplayNames/);
  assert.match(countries, /KE KG KH/);
  assert.match(contents, /name="website"/);
  assert.match(contents, /body\.website/);
  assert.match(contents, /body\.turnstile_token/);
  assert.match(contents, /body\.turnstile_token = challengeToken/);
  assert.match(contents, /challengeRequired && !challengeToken/);
  assert.match(contents, /errorCode === "bot_challenge_failed"/);
  assert.match(contents, /mode === "login" \|\| mode === "register" \? \([\s\S]*<TurnstileWidget/);
  assert.match(contents, /router\.replace\(localizedDestination\)/);
  assert.doesNotMatch(contents, /product updates|marketing_consent/i);
  assert.doesNotMatch(account, /product updates|marketing_consent/i);
  assert.match(turnstile, /action:\s*"login" \| "register"/);
  assert.match(turnstile, /"response-field": false/);
  assert.match(turnstile, /"error" \| "expired" \| "loading" \| "ready" \| "verified"/);
  assert.match(turnstile, /"error-callback"[\s\S]*updateState\("error"\)/);
  assert.match(turnstile, /"expired-callback"[\s\S]*updateState\("expired"\)/);
  assert.match(turnstile, /Retry security check/);
  assert.match(contents, /Loading security check…/);
  assert.match(contents, /Complete security check to continue/);
  for (const event of ["signup_viewed", "signup_started", "turnstile_succeeded", "turnstile_failed", "registration_succeeded", "registration_failed"]) {
    assert.ok(contents.includes(`"${event}"`), `registration should track ${event}`);
  }
  assert.match(contents, /source: "web_registration"/);
  assert.match(contents, /keepalive: true/);
  assert.doesNotMatch(contents, /trackSignupEvent\([^\n]*(?:email|displayName|countryCode)/);
  assert.match(accountProxy, /path === "\/v1\/growth\/events"/);
  assert.match(accountProxy, /passwordHashingPaths\.has\(path\) \? 8_500 : 2_800/);
});

test("dormant growth surfaces remain closed and Pro remains noindex", async () => {
  for (const path of ["notifications/page.tsx", "subscription/page.tsx", "recommendations/page.tsx", "onboarding/page.tsx"]) {
    assert.match(await source(path), /<ComingSoon/);
  }
  const pro = await source("pro/page.tsx");
  assert.match(pro, /robots:\s*\{\s*index:\s*false/);
  assert.match(pro, /coming soon/i);
  assert.doesNotMatch(pro, /<PlansAndCheckout/);
});

test("activated account navigation does not promise dormant notifications or subscriptions", async () => {
  const navigation = await source("components/account/AccountNav.tsx");
  const favorites = await source("components/account/FavoritesList.tsx");
  const authentication = await source("components/account/AuthForm.tsx");
  assert.doesNotMatch(navigation, /notifications|subscription|recommendations/i);
  assert.doesNotMatch(favorites, /Favorite alerts|notifications_enabled/);
  assert.match(authentication, /password-reset\/request/);
  assert.match(authentication, /password-reset\/confirm/);
});

test("favorite controls cover dense match, prediction, league, team, and market surfaces, and never a country", async () => {
  const button = await source("components/account/FavoriteButton.tsx");
  const predictionCard = await source("matches/[id]/MatchPredictionCard.tsx");
  const matches = await source("components/MatchesFeed.tsx");
  const predictions = await source("components/PredictionExplorer.tsx");
  const leagues = await source("leagues/[slug]/page.tsx");
  const markets = await source("predictions/markets/[marketSlug]/page.tsx");
  assert.match(button, /"competition" \| "country" \| "league" \| "market" \| "match" \| "prediction" \| "team"/);
  assert.match(button, /hideForGuests/);
  // Both still reachable from the today board: the league heart sits on the
  // group sub-header, and the match favourite sits OUTSIDE the row's anchor,
  // positioned over its right edge -- a button nested in a link would be
  // invalid and would steal the row's own target. Dropping either would take
  // "save a match" away from the densest surface that offers it.
  //
  // The COUNTRY heart is asserted absent, which reverses this test. The
  // 2026-10-15 review (UI-03/UI-09) removed it on purpose: a country is a
  // grouping, not something to follow, and the control sat inside the count
  // span where it overlapped "8 predictions". The entity type stays in the
  // union below because the account dashboard still lists country favourites
  // saved before this change -- what is forbidden is OFFERING a new one from a
  // list header, not reading an existing one.
  for (const contents of [matches, predictions]) {
    assert.doesNotMatch(contents, /entityType="country"/);
    assert.match(contents, /entityType="league"/);
    assert.match(contents, /entityType="match"/);
  }
  // The league heart takes the 36x36 hit area of review 2.2; a row keeps 44x44.
  assert.match(predictions, /entityType="league"[\s\S]{0,240}subheader/);
  assert.match(leagues, /entityType="team"/);
  assert.match(markets, /entityType="market"/);
  assert.match(predictionCard, /<FavoriteButton[\s\S]{0,240}entityType="prediction"/);
});

test("account navigation defaults home and favorite removal matches the strict API contract", async () => {
  const authentication = await source("components/account/AuthForm.tsx");
  const favorites = await source("components/account/FavoritesList.tsx");
  assert.match(authentication, /safeInternalHref\(nextPath, "\/"\)/);
  assert.match(authentication, /router\.replace\(localizedDestination\)/);
  assert.match(favorites, /JSON\.stringify\(\{ entity_id: item\.entityId, entity_type: item\.entityType \}\)/);
});
