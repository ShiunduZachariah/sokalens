import Link from "next/link";
import type { ReactNode } from "react";
import { AdSlot } from "./AdSlot";
import { KeepExploringRail } from "./LinkCard";
import { PageHeader, type StatStripEntry } from "./PageHeader";
import { PredictionExplorer, type PredictionExplorerItem } from "./PredictionExplorer";
import { PredictionSubnav } from "./PredictionSubnav";
import { ResponsibleNote } from "./ResponsibleNote";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";
import { packMarkets } from "../lib/packedMarkets";

/**
 * The one template behind every prediction list page.
 *
 * The design canvas makes this explicit: Tomorrow, Weekend, Over 2.5, BTTS and
 * Match result are not five artboards, they are one artboard (`PList`) with a
 * `view` prop, and Today is the same layout with a date strip. Before this
 * component they were five hand-written pages that had drifted apart --
 * `/predictions/weekend` had its own `weekend-league` grid and its own row
 * markup, the market hubs used `StatCard`, and only Today had the refreshed
 * design at all. Any fix to a row had to be made in four places and in practice
 * was made in one.
 *
 * So the contract is: a page fetches and shapes its own items, names its own
 * copy and figures, and hands them here. Everything a reader sees -- subnav,
 * masthead, ad slots, the filterable feed, the rail, the disclaimer -- lives in
 * this file and is identical across the five.
 */
export type PredictionListShellProps = {
  locale: Locale;
  /** Mono uppercase line above the title. */
  eyebrow: ReactNode;
  title: ReactNode;
  /** Trailing words of the title, drawn in the accent ink. */
  accent?: ReactNode;
  summary: ReactNode;
  stats?: StatStripEntry[];
  /** Trailing crumb label; "Home / <this>". Omitted when the page is top-level. */
  breadcrumb?: ReactNode;
  /**
   * The items the feed renders. Market lists are packed here rather than by each
   * caller, because the packing and the "Markets" figure have to agree and
   * hoisting it was how they came to disagree once already.
   */
  items: PredictionExplorerItem[];
  /** Rendered between the top ad slot and the feed, inside `#content`. */
  leadContent?: ReactNode;
  /** The date strip, for the pages that carry one. */
  dateStrip?: ReactNode;
  /** The strip has moved Today's URL to another day, so its pill is not current. */
  subnavNoCurrent?: boolean;
  /** Shown in place of the feed when `items` is empty. */
  emptyState: ReactNode;
  /**
   * Receives the packed market-label count, for a page that wants it in its
   * stat strip. Called before render, so it cannot be derived by the caller --
   * the packing happens here.
   */
  marketCountStat?: (count: number) => StatStripEntry;
};

export function PredictionListShell({
  accent,
  breadcrumb,
  dateStrip,
  emptyState,
  eyebrow,
  items,
  leadContent,
  locale,
  marketCountStat,
  stats,
  subnavNoCurrent,
  summary,
  title,
}: PredictionListShellProps) {
  // The whole slate's market lists are the largest thing these pages send to
  // the browser -- 61% of the inline RSC payload, measured on production -- and
  // they are almost entirely the same few labels repeated. Pack them against a
  // shared label table; PredictionExplorer unpacks on arrival. The
  // `?? [primary market]` fallback is the one the explorer already applies to
  // items without a market list, hoisted here so the packed form and the market
  // count agree.
  const { packed, table: marketTable } = packMarkets(
    items.map((item) => item.markets ?? [{ market: item.market, prediction: item.prediction, confidence: item.confidence }]),
  );
  const wireItems: PredictionExplorerItem[] = items.map(({ markets: _markets, ...item }, index) => ({
    ...item,
    packedMarkets: packed[index],
  }));

  const statStrip = marketCountStat
    ? [...(stats ?? []), marketCountStat(marketTable.marketLabels.length)]
    : stats;

  return (
    <div className="mp mp-page">
      <main className="mp__inner" id="content">
        {/*
          Ad spec 3.1: directly under the site header, above the page title. It
          used to sit below the masthead, which is neither.
        */}
        <AdSlot format="top" placement="page-top" />

        <PredictionSubnav noCurrent={subnavNoCurrent} />

        <PageHeader
          accent={accent}
          eyebrow={eyebrow}
          stats={statStrip}
          summary={summary}
          title={title}
        >
          {breadcrumb ? (
            <nav aria-label="Breadcrumb" className="mp-crumbs">
              <Link href={localePath("/", locale)}>Home</Link>
              <span aria-hidden="true">/</span>
              <span className="mp-crumbs__here">{breadcrumb}</span>
            </nav>
          ) : null}
        </PageHeader>

        {leadContent}

        {dateStrip}

        <div className="td-body">
          {items.length > 0 ? (
            <PredictionExplorer
              emptyMessage="No signals meet these filters. Try another market."
              items={wireItems}
              marketTable={marketTable}
              title="Prediction feed"
            />
          ) : (
            <div className="td-main">{emptyState}</div>
          )}

          <aside aria-label="Prediction guidance" className="td-rail">
            <KeepExploringRail locale={locale} />
            <AdSlot format="rail" placement="page-rail" />
          </aside>
        </div>

        {/*
          No trailing in-feed block here any more.
          `PredictionExplorer` owns this page's in-feed position: ad spec 3.1
          puts it "after the first competition group", inside the feed. A second
          unit below the whole body was a second in-feed unit on the same page,
          and on a short slate it landed within eight rows of the explorer's
          own -- over the one-per-eight-rows density cap of section 6.
        */}

        <div className="mp-disclaimer">
          <span aria-label="Eighteen plus only" className="mp-disclaimer__age">18+</span>
          <p>
            Statistical estimates, never guaranteed outcomes. Use predictions as analysis, not
            financial advice.
          </p>
          <Link href={localePath("/responsible-gambling", locale)}>Responsible gambling</Link>
        </div>

        <div className="mp-tail">
          <ResponsibleNote />
        </div>
      </main>
    </div>
  );
}
