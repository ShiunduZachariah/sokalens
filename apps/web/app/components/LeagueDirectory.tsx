"use client";

import Link from "next/link";
import { useDeferredValue, useId, useMemo, useState } from "react";
import { Crest } from "./Crest";
import { ChevronRightIcon } from "./match/icons";
import { useLocale } from "./locale/LocaleProvider";
import { localePath } from "../i18n/config";
import { seoLeaguePath } from "../lib/seo";

export type DirectoryLeague = {
  slug: string;
  name: string;
  country: string;
  logoUrl?: string;
};

/*
  `href` and `hasContent` used to travel on every row. Across 891 competitions
  that is pure weight in the flight payload: the href is `seoLeaguePath(slug)`
  and derivable here, and `hasContent` is now true for every row the page
  sends, because the directory filters on it before rendering.
*/

const FEATURED_COUNT = 6;

/**
 * The competition directory: featured competitions as cards, the rest as rows.
 *
 * A client component for one reason -- the search box. The directory runs to the
 * full catalogue, and a reader who knows which league they want should not have
 * to scroll a few hundred rows to reach it or pay a round trip to filter. The
 * links are real `<Link>`s and every league is in the markup on first render, so
 * a crawler sees the whole directory whether or not the filter ever runs.
 *
 * `useDeferredValue` on the query keeps typing responsive: filtering several
 * hundred rows on every keystroke is enough work to drop frames on a mid-range
 * phone, and the input must never lag behind the finger.
 */
export function LeagueDirectory({ leagues }: { leagues: DirectoryLeague[] }) {
  const id = useId();
  const { locale } = useLocale();
  const [query, setQuery] = useState("");
  const deferredQuery = useDeferredValue(query);

  const matching = useMemo(() => {
    const needle = deferredQuery.trim().toLowerCase();
    if (!needle) return leagues;
    return leagues.filter((league) =>
      `${league.name} ${league.country}`.toLowerCase().includes(needle),
    );
  }, [deferredQuery, leagues]);

  /*
   * Featured are the covered competitions, in the order the catalogue ranks
   * them, and they are only "featured" on the unfiltered view. Under a search,
   * splitting three results into a card and two rows reads as an accident --
   * the reader asked for a list of matches, so they get one.
   */
  const searching = deferredQuery.trim().length > 0;
  const featured = searching ? [] : matching.slice(0, FEATURED_COUNT);
  const featuredSlugs = new Set(featured.map((league) => league.slug));
  const rest = matching.filter((league) => !featuredSlugs.has(league.slug));

  return (
    <>
      <label className="lg-search" htmlFor={`${id}-q`}>
        <svg aria-hidden="true" fill="none" height="16" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="16">
          <circle cx="11" cy="11" r="7" />
          <path d="M20 20l-4-4" />
        </svg>
        <input
          aria-label="Search competitions or countries"
          id={`${id}-q`}
          onChange={(event) => setQuery(event.target.value)}
          placeholder="Search competitions or countries"
          type="search"
          value={query}
        />
      </label>

      {featured.length > 0 ? (
        <>
          <div className="pf-sectionhead">
            <h2>Featured competitions</h2>
          </div>
          <div className="lg-featured">
            {featured.map((league) => (
              <Link className="lg-card" href={localePath(seoLeaguePath(league.slug), locale)} key={league.slug} prefetch={false}>
                <Crest logoUrl={league.logoUrl} name={league.name} />
                <span className="lg-card__copy">
                  <strong>{league.name}</strong>
                  {/* "· Predictions live" was hardcoded here, so it was printed
                      whether or not anything was published into the league. */}
                  <span className="lg-card__meta">{league.country || "International"}</span>
                </span>
                <span aria-hidden="true" className="lg-card__chevron"><ChevronRightIcon /></span>
              </Link>
            ))}
          </div>
        </>
      ) : null}

      <div className="pf-sectionhead">
        {/*
          Not "This week in our catalogue". The list behind this heading has
          never been weekly -- it is the catalogue ordered by priority score.
          Now that every competition with fixtures is listed rather than the
          first 100, a weekly claim would be doubly untrue.
        */}
        <h2>{searching ? "Matching competitions" : "All competitions"}</h2>
        <span aria-live="polite">
          {rest.length} {rest.length === 1 ? "competition" : "competitions"}
        </span>
      </div>

      {rest.length > 0 ? (
        <ul className="lg-rows">
          {rest.map((league) => (
            <li key={league.slug}>
              <Link href={localePath(seoLeaguePath(league.slug), locale)} prefetch={false}>
                <Crest logoUrl={league.logoUrl} name={league.name} size="sm" />
                <span className="lg-row__copy">
                  <strong>{league.name}</strong>
                  <span className="lg-card__meta">{league.country || "International"}</span>
                </span>
                {/*
                  No state label here any more. It read
                  `hasContent ? "Predictions live" : "Fixtures only"` and both
                  halves were wrong: `has_content` measures whether a league has
                  any fixture, not whether anything is predicted, and a league
                  without it has no fixtures at all rather than "fixtures only".
                  The directory now lists only leagues with content, so it would
                  print the same words on all 891 rows. The design puts a
                  fixture count in this slot; the endpoint returns none, and
                  fetching one per league is several hundred round trips to
                  decorate a directory. Country is already on the line above.
                */}
                <span aria-hidden="true" className="lg-row__chevron" />
              </Link>
            </li>
          ))}
        </ul>
      ) : (
        <div className="ac-empty" aria-live="polite">
          <h3>No competition matches &ldquo;{deferredQuery.trim()}&rdquo;</h3>
          <p>Try a country name, or clear the search to see the whole catalogue.</p>
        </div>
      )}
    </>
  );
}
