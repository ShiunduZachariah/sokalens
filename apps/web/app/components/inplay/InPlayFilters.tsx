"use client";

import { useState, type ReactNode } from "react";

export type InPlayFilterKey = "all" | "live" | "soon" | "finished";

/**
 * The in-play filter row, and the thing it filters.
 *
 * Every section is rendered on the server and stays in the document; this only
 * sets a data attribute that CSS reads. That ordering is deliberate:
 *
 *  - the page keeps working with no JavaScript, showing everything, which is
 *    the correct answer when nothing can be filtered;
 *  - the live board, the upcoming list and the finished list all stay in the
 *    HTML a crawler sees, so filtering costs nothing in the index;
 *  - switching is instant, because nothing is fetched or re-rendered.
 *
 * The counts come from the server render rather than being recounted here. They
 * are the same numbers the stat strip above already shows, and two independent
 * counts of one list is how they end up disagreeing.
 */
export function InPlayFilters({
  children,
  counts,
}: {
  children: ReactNode;
  counts: { live: number; soon: number; finished: number };
}) {
  const [active, setActive] = useState<InPlayFilterKey>("all");
  const all = counts.live + counts.soon + counts.finished;

  const pills: Array<{ key: InPlayFilterKey; label: string; count: number }> = [
    { key: "all", label: "All", count: all },
    { key: "live", label: "Live", count: counts.live },
    { key: "soon", label: "Starting soon", count: counts.soon },
    { key: "finished", label: "Finished", count: counts.finished },
  ];

  return (
    <>
      <div aria-label="Filter in-play matches" className="ip-filters" role="group">
        {pills.map((pill) => (
          <button
            aria-pressed={active === pill.key}
            className="ip-filter"
            key={pill.key}
            onClick={() => setActive(pill.key)}
            type="button"
          >
            {pill.label}
            {/* The count is its own element so the 8px gap in the design is a
                real gap rather than a space inside a string, which would
                collapse differently once a label wraps. */}
            <span className="ip-filter__count">{pill.count}</span>
          </button>
        ))}
      </div>
      <div className="ip-filtered" data-filter={active}>
        {children}
      </div>
    </>
  );
}
