"use client";

import { useId, useRef, useState, type ReactNode } from "react";

/**
 * The match page's tabs.
 *
 * Replaces a sub-nav of anchor links over stacked sections. That shape meant a
 * reader scrolled past the live score to reach a prediction, and past the
 * prediction to reach the timeline, with the page growing longer as a match
 * produced events. Flashscore and Sofascore both answer one question per tab,
 * and that is what a reader arriving mid-match is doing.
 *
 * Every panel is rendered into the DOM and inactive ones are hidden with
 * `hidden`, rather than only mounting the active one. Two reasons, and the
 * first is the important one:
 *
 *  - The panels are server-rendered content passed in as children. A crawler
 *    sees the timeline and the prediction whichever tab is selected, so the
 *    page ranks on everything it contains rather than on whatever tab happened
 *    to be the default. Mounting one at a time would hide most of the page's
 *    substance behind an interaction Googlebot does not perform.
 *  - Switching is instant and keeps scroll position, with no refetch.
 *
 * Tab state is local rather than in the URL. A match page is one subject and a
 * tab is a view of it, not a separate address -- and putting it in the query
 * string would fragment the canonical URL across four variants, which is the
 * kind of thing that needs a rel=canonical to undo.
 */

export type MatchTab = {
  id: string;
  label: string;
  /** Rendered but hidden when another tab is active. */
  content: ReactNode;
};

type MatchTabsProps = {
  tabs: MatchTab[];
  /** Falls back to the first tab when absent or not present. */
  initialTabId?: string;
  label?: string;
};

export function MatchTabs({ tabs, initialTabId, label = "Match detail sections" }: MatchTabsProps) {
  const baseId = useId();
  const usable = tabs.filter(Boolean);
  const initial = usable.some((tab) => tab.id === initialTabId) ? initialTabId : usable[0]?.id;
  const [active, setActive] = useState<string | undefined>(initial);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  if (usable.length === 0) return null;

  const activeId = usable.some((tab) => tab.id === active) ? active : usable[0].id;

  // Arrow keys move between tabs, which is what a tablist is expected to do and
  // the reason this is a button list rather than links: a reader on a keyboard
  // should not have to tab through four controls to reach the fourth.
  const onKeyDown = (event: React.KeyboardEvent, index: number) => {
    const delta = event.key === "ArrowRight" ? 1 : event.key === "ArrowLeft" ? -1 : 0;
    if (delta === 0) {
      if (event.key !== "Home" && event.key !== "End") return;
    }
    event.preventDefault();
    const next =
      event.key === "Home"
        ? 0
        : event.key === "End"
          ? usable.length - 1
          : (index + delta + usable.length) % usable.length;
    const target = usable[next];
    setActive(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <>
      <div className="match-subnav" role="tablist" aria-label={label}>
        <div className="match-subnav__scroller">
          {usable.map((tab, index) => {
            const selected = tab.id === activeId;
            return (
              <button
                aria-controls={`${baseId}-panel-${tab.id}`}
                aria-selected={selected}
                className="match-subnav__tab"
                id={`${baseId}-tab-${tab.id}`}
                key={tab.id}
                onClick={() => setActive(tab.id)}
                onKeyDown={(event) => onKeyDown(event, index)}
                ref={(node) => {
                  tabRefs.current[tab.id] = node;
                }}
                role="tab"
                // Roving tabindex: the tablist is one stop, arrows move within.
                tabIndex={selected ? 0 : -1}
                type="button"
              >
                {tab.label}
              </button>
            );
          })}
        </div>
      </div>

      {usable.map((tab) => (
        <div
          aria-labelledby={`${baseId}-tab-${tab.id}`}
          hidden={tab.id !== activeId}
          id={`${baseId}-panel-${tab.id}`}
          key={tab.id}
          role="tabpanel"
          // Panels hold their own headings and focusable content, so they are
          // not focus targets themselves.
          tabIndex={-1}
        >
          {tab.content}
        </div>
      ))}
    </>
  );
}
