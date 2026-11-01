import { unstable_cache } from "next/cache";
import { fetchLeagues } from "./catalog";

/**
 * League name to catalog slug.
 *
 * A fixture row carries the league's display name but not its slug, and the
 * slug is generated upstream from more than the name -- so deriving one with
 * `seoSlug(leagueName)` would produce a URL that is usually right and
 * occasionally a 404. That is precisely the class of defect that put 37 URLs
 * in Search Console's "Not found" bucket, so the mapping is looked up rather
 * than guessed: an unknown name yields no link at all.
 *
 * Cached for an hour. Competitions are not renamed between page views, and
 * every fixture page would otherwise re-read the league list.
 */
const loadLeagueSlugsCached = unstable_cache(async (): Promise<Record<string, string>> => {
  const result = await fetchLeagues(100);
  if (!result.ok) return {};
  const byName: Record<string, string> = {};
  for (const league of result.data) {
    if (league.has_content && league.name && league.slug) byName[league.name.trim().toLowerCase()] = league.slug;
  }
  return byName;
}, ["league-slug-lookup-v1"], { revalidate: 3600 });

export async function leagueSlugForName(leagueName: string | null | undefined): Promise<string | null> {
  const key = leagueName?.trim().toLowerCase();
  if (!key) return null;
  return (await loadLeagueSlugsCached())[key] ?? null;
}
