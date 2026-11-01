"use client";

import Link from "next/link";
import { formatConfidence } from "../lib/format";
import { LocalTime } from "./LocalTime";
import { MatchupIdentity } from "./MatchupIdentity";
import { useLocale } from "./locale/LocaleProvider";
import { localePath } from "../i18n/config";
import type { BestBetItem } from "./BestBetExplorer";

/**
 * The shortlist behind a sign-up, as the 2026-10-02 canvas draws it.
 *
 * Four rows under a blur with a card over them. The important part is what is
 * NOT here: the hero pick. The design puts the top pick OUTSIDE this guard, so an
 * anonymous reader — and a crawler — gets one complete, readable, ranked pick
 * with its confidence, and the gate starts at number two.
 *
 * That split is doing real work. `/best-bets` is in the sitemap and indexable, and
 * a page whose entire content sat behind a blur would be thin content: Googlebot
 * is always anonymous, so whatever anonymous readers see is what gets indexed.
 * One free pick keeps the page worth ranking and still makes the account worth
 * creating.
 *
 * The gate is a REGISTRATION gate, not a paywall — "free with a SokaLens
 * account", per the design's own copy. So the primary action is "Create free
 * account", not an upgrade.
 */

/** How many blurred rows sit under the card. More is just more blur. */
const TEASER_ROWS = 4;

export function BestBetsLocked({
  items,
  startRank,
  total,
}: {
  /** The picks AFTER the hero, in rank order. */
  items: BestBetItem[];
  /** The rank of `items[0]`, so the blurred numbers continue from the hero. */
  startRank: number;
  total: number;
}) {
  const { locale } = useLocale();
  const teaser = items.slice(0, TEASER_ROWS);

  return (
    <div className="bb-locked">
      {/*
        `aria-hidden` and `inert`: a blurred row is decoration, and a screen
        reader or a keyboard reaching into it would announce and focus picks the
        page has decided not to show. The card below carries the whole message
        for anyone not reading the blur, which is why it is not also hidden.
      */}
      <div aria-hidden="true" className="bb-locked__teaser" inert>
        <ol className="bb-rows">
          {teaser.map((item, index) => (
            <li key={`${item.matchId}-${item.marketKey}`}>
              <span>
                {/*
                  Ranks continue from the hero rather than restarting at one: the
                  reader can see the top pick above, so a blurred "#1" under it
                  would read as a second, contradictory ranking.
                */}
                <span className="bb-row__rank">#{startRank + index}</span>
                <span className="bb-row__fixture">
                  {/*
                    The blurred rows are the real rows, so they carry the real
                    crests at the same 20px. best-bets-locked.html draws them
                    identically to best-bets.html and simply puts a blur over
                    the list -- a teaser built from a different row shape would
                    change height the moment a reader signed in.
                  */}
                  {item.homeTeam && item.awayTeam ? (
                    <MatchupIdentity
                      awayTeam={item.awayTeam}
                      awayTeamLogo={item.awayTeamLogo}
                      className="bb-row__teams"
                      homeTeam={item.homeTeam}
                      homeTeamLogo={item.homeTeamLogo}
                      layout="stacked"
                      size={20}
                    />
                  ) : (
                    <strong>{item.match}</strong>
                  )}
                  <span className="bb-row__meta">
                    {item.league}
                    {item.kickoffAt ? <> · <LocalTime timeOnly value={item.kickoffAt} /></> : null}
                  </span>
                </span>
                <span className="bb-row__pick">
                  <strong>{item.prediction}</strong>
                  <span className="bb-row__meta">{item.market}</span>
                </span>
                <span className="bb-row__conf">{formatConfidence(item.confidence)}</span>
              </span>
            </li>
          ))}
        </ol>
      </div>

      <div className="bb-locked__overlay">
        <div className="bb-locked__card">
          <span aria-hidden="true" className="bb-locked__icon">
            <svg fill="none" height="20" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="20">
              <rect height="10" rx="2" width="14" x="5" y="11" />
              <path d="M8 11V8a4 4 0 0 1 8 0v3" />
            </svg>
          </span>
          <h2>See all {total} Best Bets</h2>
          <p>
            Best Bets are free with a SokaLens account. Sign up to see every ranked pick and its
            confidence.
          </p>
          <Link className="bb-locked__cta" href={localePath("/register", locale)} prefetch={false}>
            Create free account
          </Link>
          <Link className="bb-locked__signin" href={localePath("/login", locale)} prefetch={false}>
            I already have an account · Sign in
          </Link>
        </div>
      </div>
    </div>
  );
}
