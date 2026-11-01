import { fetchPredictionApi } from "./apiClient";
import { validCatalogSlug } from "../lib/catalogSlug";
export { validCatalogSlug } from "../lib/catalogSlug";

export type LeagueSummary = {
  id: number;
  slug: string;
  name: string;
  country: string;
  sport: string;
  priority_score: number;
  logo_url?: string;
  has_content: boolean;
};

export type CatalogMatch = {
  id: number;
  league_name: string;
  home_team: string;
  away_team: string;
  kickoff_at: string;
  status: string;
  home_score?: number;
  away_score?: number;
  home_team_logo?: string;
  away_team_logo?: string;
  /**
   * Whether a pick exists for this fixture yet: "published" or "pending".
   *
   * It has been on the wire all along -- `matchSummaryResponse` emits it for
   * every match summary, including the league hub's `upcoming_matches` -- and
   * this type was the reason nothing could read it. Absent when the API has no
   * opinion, which must NOT be rendered as pending: "we have not said" and
   * "there is no pick yet" are different answers, and only the second one has
   * copy.
   */
  prediction_state?: "published" | "pending";
};

export type CatalogTeam = {
  id: number;
  slug: string;
  name: string;
  country?: string;
  logo_url?: string;
};

export type LeagueDetail = LeagueSummary & {
  upcoming_matches: CatalogMatch[];
  recent_matches: CatalogMatch[];
  teams: CatalogTeam[];
};

export type TeamDetail = {
  id: number;
  slug: string;
  name: string;
  country: string;
  league_name: string;
  /** Optional: the API omits it when the team's league could not be resolved. */
  league_slug?: string;
  logo_url?: string;
  flag_url?: string;
  next_match?: CatalogMatch;
  recent_matches: CatalogMatch[];
};

export type CatalogFailure = "not_found" | "unavailable";

export type CatalogResult<T> = { ok: true; data: T } | { ok: false; error: CatalogFailure };

/**
 * Read a catalog entity through the shared API client so requests stay bounded
 * by PREDICTION_API_TIMEOUT_MS and revalidate through Next's data cache.
 * 404 is reported separately from a degraded/error response so pages can
 * distinguish "this does not exist" from "the feed is temporarily down".
 */
async function fetchCatalogResult<T>(path: string): Promise<CatalogResult<T>> {
  try {
    const response = await fetchPredictionApi(path, { revalidate: 60 });
    if (response.status === 404) {
      return { ok: false, error: "not_found" };
    }
    if (!response.ok) {
      return { ok: false, error: "unavailable" };
    }
    const payload = (await response.json()) as T;
    if (payload === null || typeof payload !== "object") {
      return { ok: false, error: "unavailable" };
    }
    return { ok: true, data: payload };
  } catch {
    return { ok: false, error: "unavailable" };
  }
}

/**
 * The directory asks for the whole catalogue.
 *
 * This clamp used to be `Math.min(100, …)`, one of three independent caps at
 * 100 -- here, in the handler (which ignored `?limit=` altogether) and in the
 * SQL. Together they left ~790 leagues declared in the sitemap with no internal
 * link to them.
 */
export const LEAGUE_CATALOGUE_LIMIT = 2000;

export async function fetchLeagues(limit = 100): Promise<CatalogResult<LeagueSummary[]>> {
  const boundedLimit = Math.min(LEAGUE_CATALOGUE_LIMIT, Math.max(1, Math.floor(limit)));
  const result = await fetchCatalogResult<{ leagues?: LeagueSummary[] }>(
    `/v1/leagues?limit=${boundedLimit}`,
  );
  if (!result.ok) {
    return result;
  }
  return { ok: true, data: result.data.leagues ?? [] };
}

export async function fetchLeague(slug: string): Promise<CatalogResult<LeagueDetail>> {
  if (!validCatalogSlug(slug)) return { ok: false, error: "not_found" };
  return fetchCatalogResult<LeagueDetail>(`/v1/leagues/${encodeURIComponent(slug)}`);
}

export async function fetchTeam(slug: string): Promise<CatalogResult<TeamDetail>> {
  if (!validCatalogSlug(slug)) return { ok: false, error: "not_found" };
  return fetchCatalogResult<TeamDetail>(`/v1/teams/${encodeURIComponent(slug)}`);
}
