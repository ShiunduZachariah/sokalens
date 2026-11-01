"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { localePath, stripLocalePrefix } from "../i18n/config";
import { useLocale } from "./locale/LocaleProvider";

/**
 * The eight prediction destinations, in the order the design puts them.
 *
 * This replaced `PredictionQuickFilters`, which carried five of these and
 * omitted the three that matter most for internal linking: Today (the page a
 * reader on a market hub most wants to get back to), Best Bets (the only
 * ranked view) and Leagues (the entry point to ~every league hub on the site).
 *
 * That omission was an SEO problem rather than a cosmetic one. Every one of
 * these pages is a crawl entry point, and before this component the market
 * hubs linked to each other but not back into the league tree at all, so
 * Googlebot reaching `/predictions/markets/btts` from a fixture page found no
 * route onward except the header. Eight reciprocal links on every prediction
 * page is the cheapest internal-linking fix available to us.
 *
 * `prefetch={false}` is deliberate and pinned by `navigationPrefetch.test.mjs`:
 * eight links on a high-fanout page would otherwise have Next speculatively
 * fetch eight route payloads on every prediction page view.
 */
const DESTINATIONS = [
  { href: "/predictions/today", label: "Today" },
  { href: "/predictions/tomorrow", label: "Tomorrow" },
  { href: "/predictions/weekend", label: "Weekend" },
  { href: "/best-bets", label: "Best Bets" },
  { href: "/predictions/markets/over-2-5-goals", label: "Over 2.5" },
  { href: "/predictions/markets/btts", label: "BTTS" },
  { href: "/predictions/markets/match-result", label: "Match result" },
  { href: "/leagues", label: "Leagues" },
] as const;

export function PredictionSubnav() {
  const pathname = usePathname();
  const activePath = stripLocalePrefix(pathname);
  const { locale } = useLocale();

  return (
    <nav aria-label="Prediction pages" className="td-subnav">
      {DESTINATIONS.map((destination) => {
        const current = activePath === destination.href;
        return (
          <Link
            aria-current={current ? "page" : undefined}
            className="td-subnav__pill"
            href={localePath(destination.href, locale)}
            key={destination.href}
            prefetch={false}
          >
            {destination.label}
          </Link>
        );
      })}
    </nav>
  );
}
