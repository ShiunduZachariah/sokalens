"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { marketLabel, marketSelectionLabel } from "../lib/markets";
import { useOddsFormat } from "./OddsFormatControl";
import { ShareButton } from "./ShareButton";
import { useLocale } from "./locale/LocaleProvider";
import {
  BUILDER_EVENT_NAME,
  formatOdds,
  encodeBuilderSelection,
  pruneBuilderSelection,
  totalOdds,
  writeBuilderSelection,
  type BuilderLeg,
} from "../multi-match-builder/builder-state";

function TicketIcon() {
  return (
    <svg aria-hidden="true" viewBox="0 0 24 24" width="20" height="20" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
      <path d="M3 8a2 2 0 0 1 2-2h14a2 2 0 0 1 2 2v2a2 2 0 0 0 0 4v2a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-2a2 2 0 0 0 0-4V8Z" />
      <path d="M13 6v2M13 12v2M13 18v-2" />
    </svg>
  );
}

/**
 * Bookie-style bet slip toggle + drawer. Reads the builder's sessionStorage
 * legs directly so it works from any page, and shows the accumulated odds.
 */
export function BetSlip() {
  const [legs, setLegs] = useState<BuilderLeg[]>([]);
  const [open, setOpen] = useState(false);
  const [oddsFormat] = useOddsFormat();
  const { locale } = useLocale();

  useEffect(() => {
    const update = () => setLegs(pruneBuilderSelection().active);
    update();
    const interval = window.setInterval(update, 1_000);
    window.addEventListener(BUILDER_EVENT_NAME, update);
    window.addEventListener("storage", update);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener(BUILDER_EVENT_NAME, update);
      window.removeEventListener("storage", update);
    };
  }, []);

  const accumulated = totalOdds(legs);
  const shareText = ["My SokaLens Builder slip", ...legs.map((leg, index) => `${index + 1}. ${leg.matchTitle} — ${marketLabel(leg.marketKey, locale)}: ${marketSelectionLabel(leg.marketKey, leg.selection, locale)}`), "Statistical analysis, not a guarantee."].join("\n");

  const clear = () => {
    writeBuilderSelection([]);
    setLegs([]);
  };

  return (
    <>
      <button
        type="button"
        className="bet-slip-toggle"
        aria-expanded={open}
        aria-label={legs.length > 0 ? `Bet slip, ${legs.length} selections` : "Bet slip"}
        onClick={() => setOpen((current) => !current)}
      >
        <TicketIcon />
        <span className="bet-slip-toggle__label">Slip</span>
        {legs.length > 0 ? <span className="bet-slip-toggle__badge">{legs.length}</span> : null}
      </button>

      {open ? (
        <div className="bet-slip-drawer" role="dialog" aria-label="Bet slip">
          <div className="bet-slip-drawer__header">
            <strong>Bet slip</strong>
            <button type="button" className="bet-slip-drawer__close" onClick={() => setOpen(false)}>
              Close
            </button>
          </div>

          {legs.length > 0 ? (
            <>
              <ul className="bet-slip-drawer__legs">
                {legs.map((leg) => (
                  <li className="bet-slip-drawer__leg" key={leg.id}>
                    <div>
                      <strong>{leg.matchTitle}</strong>
                      {leg.groupLabel ? <span className="micro">System ACCA · {leg.groupLabel}</span> : null}
                      <span className="muted">
                        {marketLabel(leg.marketKey, locale)} / {marketSelectionLabel(leg.marketKey, leg.selection, locale)}
                      </span>
                    </div>
                    <strong className="bet-slip-drawer__odds">{formatOdds(leg.odds, oddsFormat)}</strong>
                  </li>
                ))}
              </ul>

              <div className="bet-slip-drawer__total">
                <span>Total odds</span>
                <strong>{accumulated != null ? formatOdds(accumulated, oddsFormat) : "—"}</strong>
              </div>

              <div className="bet-slip-drawer__actions">
                <ShareButton compact entityId={legs.map((leg) => leg.id).join("~")} entityType="builder" path={`/multi-match-builder?ticket=${encodeURIComponent(encodeBuilderSelection(legs))}`} text={shareText} title="SokaLens Builder slip" />
                <Link className="button" href="/multi-match-builder" onClick={() => setOpen(false)}>
                  Open builder
                </Link>
                <button type="button" className="button button--secondary" onClick={clear}>
                  Clear
                </button>
              </div>
            </>
          ) : (
            <p className="muted bet-slip-drawer__empty">
              Your ticket is empty. Build it market by market, or explicitly add a system ACCA.
            </p>
          )}
        </div>
      ) : null}
    </>
  );
}
