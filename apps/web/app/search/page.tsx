import { EmptyState } from "../components/EmptyState";
import { SearchBox } from "../components/SearchBox";
import { BackButton } from "../components/BackButton";
import { fetchPredictionApi } from "../data/apiClient";
import { fetchTodayMatches } from "../data/liveMatches";
import { entityTypeLabel } from "../lib/entityType";

import { canonicalPath } from "../site";
import { searchHitHref } from "../lib/searchHref";
import { fixtureHref } from "../lib/fixtureHref";
import { hasImage, imageSrc } from "../lib/imageSrc";

import { LocalTime } from "../components/LocalTime";
type SearchPageProps = {
  searchParams?: Promise<Record<string, string | string[] | undefined>>;
};

type SearchResult = {
  type: string;
  id: number;
  key?: string;
  match_id?: number;
  code?: string;
  name: string;
  slug?: string;
  subtitle: string;
  kickoff_at?: string;
  status?: string;
  logo_url?: string;
};

export const metadata = {
  title: "Search football teams, leagues, and matches - SokaLens",
  description: "Search SokaLens for football teams, leagues, competitions, countries, and matches.",
  alternates: {
    canonical: canonicalPath("/search"),
  },
  robots: { index: false, follow: false },
};

export default async function SearchPage({ searchParams }: SearchPageProps) {
  const params = searchParams ? await searchParams : {};
  const rawQuery = params.q;
  const query = Array.isArray(rawQuery) ? rawQuery[0] ?? "" : rawQuery ?? "";
  const normalizedQuery = query.trim().toLowerCase();
  let results: SearchResult[] = [];
  let searchFailed = false;
  let popular: { id: number; homeTeam: string; awayTeam: string; leagueName: string; kickoffAt: string }[] = [];
  if (normalizedQuery.length >= 2) {
    try {
      const response = await fetchPredictionApi(
        `/v1/search?q=${encodeURIComponent(query.trim())}&limit=20&include_recommendations=true`,
        { revalidate: 60 },
      );
      if (response.ok) {
        const payload = (await response.json()) as { results?: SearchResult[] };
        results = payload.results ?? [];
      } else {
        searchFailed = true;
      }
    } catch {
      searchFailed = true;
    }
  } else {
    // Empty search: recommend upcoming matches so the page is never blank.
    try {
      popular = (await fetchTodayMatches(8)).map((match) => ({
        id: match.id,
        homeTeam: match.homeTeam,
        awayTeam: match.awayTeam,
        leagueName: match.leagueName,
        kickoffAt: match.kickoffAt,
      }));
    } catch {
      popular = [];
    }
  }

  return (
    <main className="page-shell" id="content">
      <div className="main-column">
        <section>
          <BackButton />
          <p className="page-kicker">Search</p>
          <h1 className="page-title">Find teams, leagues, competitions, and matches.</h1>
          <p className="page-summary">
            Suggestions appear as you type, including partial team names and upcoming matchups.
          </p>
        </section>

        <div className="detail-panel">
          <SearchBox initialQuery={query} key={query} />
        </div>

        <section className="section" aria-labelledby="search-results">
          <div className="section-header">
            <div>
              <h2 id="search-results">{normalizedQuery ? "Search results" : "Search"}</h2>
              <p>
                {normalizedQuery.length < 2
                  ? "Type at least two characters to search."
                  : `${results.length} ${results.length === 1 ? "result" : "results"} across all entities.`}
              </p>
            </div>
          </div>

          {searchFailed ? (
            <EmptyState
              message="Search is temporarily unavailable. Try again shortly or browse from the homepage."
              title="Search unavailable"
            />
          ) : normalizedQuery.length >= 2 && results.length === 0 ? (
            <EmptyState
              message="Try a team, league, country, or match name from the current SokaLens index."
              title="No search results"
            />
          ) : normalizedQuery.length < 2 && popular.length > 0 ? (
            <div className="search-result-list">
              {popular.map((match) => (
                <a className="search-result-row" href={fixtureHref(match)} key={match.id}>
                  <span className="search-result-row__type">Match</span>
                  <span className="search-result-row__copy"><strong>{match.homeTeam} vs {match.awayTeam}</strong><span>{match.leagueName}</span></span>
                  <span className="search-result-row__meta"><LocalTime value={match.kickoffAt} /></span>
                </a>
              ))}
            </div>
          ) : (
            <div className="search-result-list">
              {results.map((result) => (
                <a className="search-result-row" href={searchHitHref(result)} key={`${result.type}-${result.key ?? result.id}`}>
                  <span className="search-result-row__identity">
                    {hasImage(result.logo_url) ? (
                      // eslint-disable-next-line @next/next/no-img-element
                      <img alt="" className="entity-thumb" decoding="async" height={32} loading="lazy" src={imageSrc(result.logo_url)} width={32} />
                    ) : null}
                    <span className="search-result-row__type">{entityTypeLabel(result.type)}</span>
                  </span>
                  <span className="search-result-row__copy"><strong>{result.name}</strong><span>{result.subtitle}</span></span>
                  <span className="search-result-row__meta">{result.kickoff_at ? <LocalTime value={result.kickoff_at} /> : "View details"}</span>
                </a>
              ))}
            </div>
          )}
        </section>
      </div>
    </main>
  );
}
