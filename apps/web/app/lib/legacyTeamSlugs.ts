/**
 * Former team URLs observed as 404s in Search Console on 19 October 2026.
 * Each replacement was checked against the production team catalogue. These
 * are exact, reviewed slug redirects; a name alone is not an identity rule.
 * The ambiguous Hang Yuen URL is deliberately absent.
 */
const redirects: Record<string, string> = {
  "team-anderlecht": "team-belgium-anderlecht",
  "team-bilje": "team-slovenia-bilje",
  "team-dravinja": "team-slovenia-dravinja",
  "team-drita": "team-kosovo-drita",
  "team-ibiza-islas-pitiusas": "team-spain-ibiza-islas-pitiusas",
  "team-interclube": "team-angola-interclube",
  "team-kabuscorp": "team-angola-kabuscorp",
  "team-le-messager-ngozi": "team-burundi-le-messager-ngozi",
  "team-machida-zelvia": "team-japan-machida-zelvia",
  "team-olimpija-ljubljana": "team-slovenia-olimpija-ljubljana",
  "team-primorje": "team-slovenia-primorje",
  "team-sagadam": "team-turkmenistan-sagadam",
  "team-st-gallen-ii": "team-switzerland-st-gallen-ii",
  "team-westfalia-kinderhaus": "team-germany-westfalia-kinderhaus",
  "team-world-albania-u21": "team-albania-albania-u21",
  "team-world-dinamo-tirana": "team-albania-dinamo-tirana",
  "team-world-home-united": "team-singapore-home-united",
  "team-world-machida-zelvia": "team-japan-machida-zelvia",
  "team-world-manresa": "team-spain-manresa",
  "team-world-mjallby-aif": "team-sweden-mjallby-aif",
  "team-world-oh-leuven-w": "team-belgium-oh-leuven-w",
  "team-world-pakhtakor": "team-uzbekistan-pakhtakor",
  "team-world-regar-tadaz": "team-tajikistan-regar-tadaz",
  "team-world-sant-andreu": "team-spain-sant-andreu",
  "team-world-shabab-al-ahli-dubai": "team-united-arab-emirates-shabab-al-ahli-dubai",
  "team-world-sturm-graz-w": "team-austria-sturm-graz-w",
  "team-world-tainan-city": "team-chinese-taipei-tainan-city",
  "team-world-xerez-deportivo": "team-spain-xerez-deportivo",
};

export function legacyTeamCanonicalSlug(slug: string): string {
  return redirects[slug] ?? slug;
}
