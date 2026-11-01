// Ranked global competitions, keyed on `COUNTRY|name`.
//
// This was previously a substring match on league name alone, which meant any
// league CONTAINING a famous name inherited its rank: "Kenyan Premier League"
// scored the same as England's, so Gor Mahia led the homepage worldwide. League
// names are not unique here -- "Premier League" also exists for AM, BB, BD, BH,
// BM and BT, "Bundesliga" for AT, "Ligue 1" for BF -- so the country is what
// makes the match trustworthy.
const rankedLeagues = [
  "|champions league",       // continental competitions carry no country
  "GB|premier league",
  "ES|la liga",
  "IT|serie a",
  "DE|bundesliga",
  "FR|ligue 1",
  "|europa league",
  "|conference league",
  "PT|primeira liga",
  "NL|eredivisie",
  "US|major league soccer",
  "BR|serie a",
  "AR|liga profesional argentina",
];

/** Continental competitions are matched on name because they have no country. */
function rankedLeagueKey(country: string, league: string) {
  const name = league.trim().toLowerCase();
  const code = country.trim().toUpperCase();
  const continental = rankedLeagues.find(
    (entry) => entry.startsWith("|") && name.includes(entry.slice(1)),
  );
  if (continental) return continental;
  return /^[A-Z]{2}$/.test(code) ? `${code}|${name}` : "";
}

const popularCountries = [
  "International", "England", "Scotland", "Wales", "Northern Ireland", "ES", "IT", "DE", "FR", "PT", "NL", "US",
];

const homeNationPatterns = [
  { label: "Scotland", pattern: /\b(scotland|scottish|spfl)\b/i },
  { label: "Wales", pattern: /\b(wales|welsh|cymru)\b/i },
  { label: "Northern Ireland", pattern: /\b(northern ireland|nifl|irish premiership)\b/i },
] as const;

/**
 * Which football association a UK competition belongs to.
 *
 * The provider sends one ISO code for the whole United Kingdom, so England,
 * Scotland, Wales and Northern Ireland all arrive as `GB` and the home nation
 * has to be read off the competition itself.
 *
 * Reading it off the NAME alone is not enough, and Wales is the case that
 * proves it: its top division is called "Premier League", exactly like
 * England's, so the patterns never matched and every Welsh fixture was filed
 * under England. The SLUG does carry it -- `league-wales-premier-league` -- so
 * both are searched. Callers that have no slug behave exactly as before.
 */
export function footballCountry(country: string, league = "", slug = "") {
  const normalized = country.trim();
  if (!["GB", "GBR", "UK", "United Kingdom"].includes(normalized)) return normalized;
  // Hyphens become spaces first, or the two-word nations never match: the
  // slug says `league-northern-ireland-premiership` and the pattern looks for
  // "northern ireland".
  const haystack = `${league} ${slug}`.replace(/-/g, " ");
  return homeNationPatterns.find((item) => item.pattern.test(haystack))?.label ?? "England";
}

export function competitionPopularity(country: string, league: string, slug = "") {
  const leagueRank = rankedLeagues.indexOf(rankedLeagueKey(country, league));
  if (leagueRank >= 0) return leagueRank;
  const countryRank = popularCountries.indexOf(footballCountry(country, league, slug));
  return 100 + (countryRank >= 0 ? countryRank : popularCountries.length);
}

export function countryLabel(country: string, league = "", slug = "") {
  const footballAssociation = footballCountry(country, league, slug);
  if (!/^[A-Z]{2}$/.test(footballAssociation)) return footballAssociation;
  try {
    return new Intl.DisplayNames(["en"], { type: "region" }).of(footballAssociation) ?? footballAssociation;
  } catch {
    return footballAssociation;
  }
}

export function compareCompetitions(
  left: { country: string; league: string; slug?: string },
  right: { country: string; league: string; slug?: string },
) {
  const popularity = competitionPopularity(left.country, left.league, left.slug) - competitionPopularity(right.country, right.league, right.slug);
  if (popularity !== 0) return popularity;
  const country = footballCountry(left.country, left.league, left.slug).localeCompare(footballCountry(right.country, right.league, right.slug));
  return country !== 0 ? country : left.league.localeCompare(right.league);
}
