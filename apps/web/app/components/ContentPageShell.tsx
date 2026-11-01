import Link from "next/link";
import { Fragment, type ReactNode } from "react";
import { AdSlot } from "./AdSlot";
import { FeedAdBlock } from "./FeedAdBlock";
import { PageHeader, type StatStripEntry } from "./PageHeader";
import { ResponsibleNote } from "./ResponsibleNote";
import type { Locale } from "../i18n/config";
import { localePath } from "../i18n/config";

/**
 * The chrome shared by the refreshed pages that are not prediction lists:
 * `/results`, `/leagues`, a league hub, a team page.
 *
 * The same shape as `PredictionListShell` -- masthead, top slot, 8/4 body grid,
 * rail, in-feed slot, disclaimer -- but without the prediction subnav or the
 * filterable feed, because neither belongs on a directory or an accuracy
 * record. Two shells rather than one with a `kind` prop: the list shell owns the
 * market packing and the explorer, and threading those through a page that has
 * no feed would make both harder to read than keeping them apart.
 *
 * The rail is supplied by the caller rather than fixed, because unlike the
 * prediction lists these pages have genuinely different onward journeys -- a
 * team page offers that team's league, an accuracy record offers today's picks.
 */
export type ContentPageShellProps = {
  locale: Locale;
  eyebrow?: ReactNode;
  title: ReactNode;
  accent?: ReactNode;
  summary?: ReactNode;
  stats?: StatStripEntry[];
  /**
   * The crumbs after "Home", in order. The last one is the current page and is
   * rendered as text; any earlier one with an `href` becomes a link.
   *
   * A list rather than a single node because a league hub's trail is three deep
   * (Home / Leagues / Premier League) and a team's is four, and expressing that
   * by passing pre-built markup put the intermediate crumb inside the
   * current-page span -- styled as "here" and nested in another span.
   */
  crumbs?: Array<{ label: ReactNode; href?: string }>;
  /** Rendered between the masthead and the body grid, full width. */
  beforeBody?: ReactNode;
  children: ReactNode;
  rail?: ReactNode;
};

export function ContentPageShell({
  accent,
  beforeBody,
  children,
  crumbs,
  eyebrow,
  locale,
  rail,
  stats,
  summary,
  title,
}: ContentPageShellProps) {
  return (
    <div className="mp mp-page">
      <main className="mp__inner" id="content">
        {/*
          Ad spec 3.1 puts the top slot "directly under the site header, above
          the page title". It used to render below this masthead, which is a
          3.2 violation twice over: it is not under the header, and on mobile it
          put a second unit on the first screen once the adhesion band filled.
        */}
        <AdSlot format="top" placement="page-top" />

        <PageHeader
          accent={accent}
          eyebrow={eyebrow}
          stats={stats}
          summary={summary}
          title={title}
        >
          {crumbs && crumbs.length > 0 ? (
            <nav aria-label="Breadcrumb" className="mp-crumbs">
              <Link href={localePath("/", locale)}>Home</Link>
              {crumbs.map((crumb, index) => (
                <Fragment key={index}>
                  <span aria-hidden="true">/</span>
                  {crumb.href && index < crumbs.length - 1 ? (
                    <Link href={localePath(crumb.href, locale)}>{crumb.label}</Link>
                  ) : (
                    <span className="mp-crumbs__here">{crumb.label}</span>
                  )}
                </Fragment>
              ))}
            </nav>
          ) : null}
        </PageHeader>

        {beforeBody}

        <div className="td-body">
          <div className="td-main">{children}</div>
          {rail ? (
            <aside aria-label="Keep exploring" className="td-rail">
              {rail}
              <AdSlot format="rail" placement="page-rail" />
            </aside>
          ) : null}
        </div>

        <FeedAdBlock />

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
