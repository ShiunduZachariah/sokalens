"use client";

import { useEffect, useId, useState } from "react";
import { accountApiRequest } from "../data/accountApi";
import { ODDS_FORMAT_EVENT, oddsFormatLabels, readOddsFormat, writeOddsFormat, type OddsFormat } from "../lib/odds";
import { useAccount } from "./account/AccountContext";

export function useOddsFormat(): [OddsFormat, (format: OddsFormat) => void] {
  const { access, refreshProfile } = useAccount();
  const [format, setFormat] = useState<OddsFormat>("decimal");

  useEffect(() => {
    const update = () => setFormat(readOddsFormat());
    update();
    window.addEventListener(ODDS_FORMAT_EVENT, update);
    window.addEventListener("storage", update);
    return () => {
      window.removeEventListener(ODDS_FORMAT_EVENT, update);
      window.removeEventListener("storage", update);
    };
  }, []);

  const persist = (next: OddsFormat) => {
    setFormat(next);
    writeOddsFormat(next);
    if (access?.authenticated && access.csrfToken) {
      void accountApiRequest<unknown>("/v1/me/profile", {
        body: JSON.stringify({ odds_format: next }),
        headers: { "X-CSRF-Token": access.csrfToken },
        method: "PATCH",
      }).then((result) => {
        if (result.ok) void refreshProfile();
      });
    }
  };
  return [format, persist];
}

/**
 * A segmented control, per the design: three equal columns in a bordered track,
 * the selected one filled with the accent. The design carries no visible label
 * -- the group is named for assistive tech only -- because the three words say
 * what they are.
 *
 * Built on real radio inputs rather than the design's `aria-pressed` buttons.
 * They render identically, and this is a single choice from three, which is
 * what a radio group is: `aria-pressed` would announce three independent
 * toggles ("Decimal, pressed") and would not give arrow-key navigation between
 * them. The browser does both for free here.
 */
export function OddsFormatControl({ value, onChange }: { value: OddsFormat; onChange: (format: OddsFormat) => void }) {
  const name = `odds-format-${useId()}`;
  return (
    <div aria-label="Odds format" className="ofc" role="group">
      {(Object.keys(oddsFormatLabels) as OddsFormat[]).map((format) => (
        <label className="ofc__segment" key={format}>
          <input
            checked={value === format}
            className="ofc__input"
            name={name}
            onChange={() => onChange(format)}
            type="radio"
            value={format}
          />
          <span className="ofc__label">{oddsFormatLabels[format]}</span>
        </label>
      ))}
    </div>
  );
}
