import type { ReactNode } from "react";

export type StatStripEntry = {
  label: string;
  value: ReactNode;
  /** Draws the figure in the accent ink. The design accents exactly one cell. */
  accent?: boolean;
};

/**
 * The design's labelled-figure strip: a bordered row of `label / figure` cells.
 *
 * A `<dl>` because that is what these are -- labelled quantities -- and not
 * three cards that happen to sit in a row. Screen readers announce the pairing;
 * three `<div>`s would announce six unrelated strings.
 *
 * Renders nothing when given no entries, so a page whose figures all failed to
 * load shows no empty bordered box.
 */
export function StatStrip({ entries }: { entries: StatStripEntry[] }) {
  if (entries.length === 0) return null;
  return (
    <dl className="td-totals">
      {entries.map((entry) => (
        <div key={entry.label}>
          <dt>{entry.label}</dt>
          <dd className={entry.accent ? "td-totals__accent" : undefined}>{entry.value}</dd>
        </div>
      ))}
    </dl>
  );
}

export type PageHeaderProps = {
  /** Mono uppercase line above the title; the design prefixes it with a dot. */
  eyebrow?: ReactNode;
  title: ReactNode;
  /**
   * Trailing words of the title, drawn in the accent ink.
   *
   * Separate from `title` rather than embedded markup because the accent is
   * `--sl-accent-ink`, not `--sl-accent`: the lime measures 1.3:1 on the light
   * theme's white and is unreadable as text. Keeping it a prop means no page can
   * reach for the fill colour by hand.
   */
  accent?: ReactNode;
  summary?: ReactNode;
  stats?: StatStripEntry[];
  /** Breadcrumbs or other chrome, rendered above the eyebrow. */
  children?: ReactNode;
};

/**
 * The page masthead shared by every refreshed list page.
 *
 * The title's extreme condensation (`font-stretch: 62%`) is load-bearing rather
 * than decorative: at 96px nothing shorter fits "Both teams to score" on one
 * line, and the design's whole vertical rhythm assumes it does. It needs the
 * Archivo variable font's `wdth` axis, which `globals.css` loads.
 */
export function PageHeader({ eyebrow, title, accent, summary, stats, children }: PageHeaderProps) {
  return (
    <section className="td-head">
      <div className="td-head__copy">
        {children}
        {eyebrow ? <span className="hm-hero__eyebrow">{eyebrow}</span> : null}
        <h1>
          {title}
          {accent ? <> <em>{accent}</em></> : null}
        </h1>
        {summary ? <p className="td-head__note">{summary}</p> : null}
      </div>
      {stats ? <StatStrip entries={stats} /> : null}
    </section>
  );
}
