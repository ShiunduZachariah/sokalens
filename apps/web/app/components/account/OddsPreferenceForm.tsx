"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import { accountApiRequest, safeInternalHref } from "../../data/accountApi";
import { oddsFormatLabels, writeOddsFormat, type OddsFormat } from "../../lib/odds";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

const examples: Record<OddsFormat, string> = {
  decimal: "2.50",
  fractional: "3/2",
  american: "+150",
};

export function OddsPreferenceForm({ nextPath }: { nextPath?: string }) {
  const router = useRouter();
  const { access, profile, profileStatus, refreshProfile, status } = useAccount();
  const [format, setFormat] = useState<OddsFormat>(
    profile?.oddsFormat === "fractional" || profile?.oddsFormat === "american" ? profile.oddsFormat : "decimal",
  );
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (profile?.oddsFormat === "decimal" || profile?.oddsFormat === "fractional" || profile?.oddsFormat === "american") {
      setFormat(profile.oddsFormat);
    }
  }, [profile?.oddsFormat]);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!access?.csrfToken || busy) return;
    setBusy(true);
    setError(undefined);
    const result = await accountApiRequest<unknown>("/v1/me/profile", {
      body: JSON.stringify({ odds_format: format }),
      headers: { "X-CSRF-Token": access.csrfToken },
      method: "PATCH",
    });
    if (!result.ok) {
      setBusy(false);
      setError(result.message);
      return;
    }
    writeOddsFormat(format);
    await refreshProfile();
    const destination = safeInternalHref(nextPath, "/account");
    router.replace(destination === "/odds-preference" ? "/account" : destination);
    router.refresh();
  }

  if (status === "ready" && !access?.authenticated) {
    return (
      <main className="preference-page" id="content">
        <section className="preference-panel">
          <h1>Sign in first</h1>
          <p>Your odds preference is saved with your account.</p>
          <Link className="button" href="/login?next=/odds-preference">Sign in</Link>
        </section>
      </main>
    );
  }

  return (
    <main className="preference-page" id="content">
      <section className="preference-panel" aria-labelledby="odds-preference-title">
        <p className="page-kicker">One quick choice</p>
        <h1 id="odds-preference-title">How do you read odds?</h1>
        <p>Choose the format you know. It will change odds across SokaLens immediately, and you can change it later in Account.</p>
        <form onSubmit={submit}>
          <fieldset className="odds-preference-grid" disabled={busy || profileStatus === "loading" || !access?.csrfToken}>
            <legend className="sr-only">Preferred odds format</legend>
            {(Object.keys(oddsFormatLabels) as OddsFormat[]).map((value) => (
              <label className="odds-preference-card" key={value}>
                <input checked={format === value} name="odds_format" onChange={() => setFormat(value)} type="radio" value={value} />
                <span><strong>{oddsFormatLabels[value]}</strong><small>Example: {examples[value]}</small></span>
              </label>
            ))}
          </fieldset>
          {error ? <InlineStatus message={error} tone="error" /> : null}
          <button className="button button--wide" disabled={busy || !access?.csrfToken} type="submit">
            {busy ? "Saving…" : "Use this odds format"}
          </button>
        </form>
      </section>
    </main>
  );
}
