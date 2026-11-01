"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  readNumber,
  readString,
} from "../../data/accountApi";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

type AuthorizedAnalysis = {
  confidence?: number;
  market?: string;
  pick?: string;
  reasoning: string[];
  summary?: string;
  tier?: string;
};

function normalizeAnalysis(payload: unknown): AuthorizedAnalysis {
  const root = asRecord(payload);
  const source = Object.keys(asRecord(root.analysis)).length ? asRecord(root.analysis) : root;
  const prediction = asRecord(source.prediction);
  const rawReasoning = source.reasoning ?? source.reasons ?? source.explanation;
  const reasoning = Array.isArray(rawReasoning)
    ? rawReasoning.map((value) => typeof value === "string" ? value : readString(asRecord(value), "text", "reason")).filter((value): value is string => Boolean(value))
    : typeof rawReasoning === "string" ? [rawReasoning] : [];
  return {
    confidence: readNumber(source, "confidence", "probability") ?? readNumber(prediction, "confidence", "probability"),
    market: readString(source, "market", "market_label") ?? readString(prediction, "market", "market_label"),
    pick: readString(source, "pick", "selection") ?? readString(prediction, "pick", "selection"),
    reasoning,
    summary: readString(source, "summary", "analysis_summary"),
    tier: readString(root, "projection_tier", "tier") ?? readString(source, "tier"),
  };
}

function isLocked(status: number, code?: string) {
  return status === 401 || status === 403 || status === 429 || code === "guest_limit_reached" || code === "entitlement_required";
}

export function GuestAnalysisMeter() {
  const { access, status } = useAccount();
  if (status !== "ready" || access?.authenticated || access?.guestAnalysisRemaining === undefined) return null;
  return (
    <span className={access.guestAnalysisRemaining > 0 ? "pill pill--signal" : "pill pill--gold"}>
      {access.guestAnalysisRemaining} free detailed {access.guestAnalysisRemaining === 1 ? "view" : "views"} left
    </span>
  );
}

export function AnalysisAccessPanel({ available = true, matchId }: { available?: boolean; matchId: number }) {
  const { access, refreshAccess, status } = useAccount();
  const [analysis, setAnalysis] = useState<AuthorizedAnalysis>();
  const [loading, setLoading] = useState(available);
  const [locked, setLocked] = useState(false);
  const [error, setError] = useState<string | undefined>(available ? undefined : "Deeper analysis has not been prepared for this match yet.");

  useEffect(() => {
    if (!available) {
      setAnalysis(undefined);
      setLocked(false);
      setError("Deeper analysis has not been prepared for this match yet.");
      setLoading(false);
      return;
    }
    if (status === "loading") return;
    if (status === "unavailable") {
      setAnalysis(undefined);
      setLocked(false);
      setError("Deeper analysis is temporarily unavailable. The public prediction above is still current.");
      setLoading(false);
      return;
    }
    setAnalysis(undefined);
    setLocked(false);
    setError(undefined);
    setLoading(true);
    let active = true;
    void accountApiRequest<unknown>(`/v1/analysis/${matchId}`).then(async (result) => {
      if (!active) return;
      if (result.ok) {
        setLocked(false);
        setError(undefined);
        setAnalysis(normalizeAnalysis(result.data));
        await refreshAccess();
      } else if (isLocked(result.status, result.code)) {
        setAnalysis(undefined);
        setError(undefined);
        setLocked(true);
      } else if (result.status === 404) {
        setAnalysis(undefined);
        setLocked(false);
        setError("Deeper analysis has not been prepared for this match yet.");
      } else {
        setAnalysis(undefined);
        setLocked(false);
        setError(result.message);
      }
      setLoading(false);
    });
    return () => { active = false; };
  }, [available, matchId, refreshAccess, status]);

  return (
    <section className="analysis-access" id="deep-analysis" aria-labelledby="deep-analysis-heading">
      <div className="analysis-access__header">
        <div>
          <span className="eyebrow">Match analysis access</span>
          <h2 id="deep-analysis-heading">Deeper match context</h2>
        </div>
        <GuestAnalysisMeter />
      </div>
      {loading ? <LoadingSkeleton count={1} label="Checking analysis access" variant="card" /> : null}
      {error ? <InlineStatus message={error} tone="info" /> : null}
      {locked ? (
        <div className="locked-analysis">
          <div className="locked-analysis__icon" aria-hidden="true">↗</div>
          <div>
            <span className="pill pill--gold">Access limit reached</span>
            <h3>{access?.authenticated ? "This analysis needs a higher capability." : "Keep exploring with a free account."}</h3>
            <p>{access?.authenticated ? "Your current plan does not include this level of analysis." : "You have used today’s free detailed views. Create a free account for standard analysis and saved preferences."}</p>
          </div>
          <div className="locked-analysis__actions">
            <Link className="button" href={access?.authenticated ? "/pro" : `/register?next=/matches/${matchId}`}>{access?.authenticated ? "Compare plans" : "Create free account"}</Link>
            {!access?.authenticated ? <Link className="button button--secondary" href={`/login?next=/matches/${matchId}`}>Sign in</Link> : null}
          </div>
        </div>
      ) : null}
      {analysis ? (
        <article className="authorized-analysis">
          <div className="authorized-analysis__summary">
            <div>
              <span className="micro">{analysis.market ?? "Prepared match analysis"}</span>
              <h3>{analysis.pick ?? "Analysis available"}</h3>
              {analysis.summary ? <p>{analysis.summary}</p> : null}
            </div>
            {analysis.confidence !== undefined ? <strong aria-label={`${Math.round(analysis.confidence)} percent confidence`}>{Math.round(analysis.confidence)}%</strong> : null}
          </div>
          {analysis.reasoning.length ? (
            <ul className="analysis-reasons">{analysis.reasoning.map((reason) => <li key={reason}>{reason}</li>)}</ul>
          ) : <p className="muted">No additional reasoning text was included in this authorized projection.</p>}
          <p className="micro">Analysis level: {analysis.tier ?? access?.tier ?? "standard"}. Access follows your current account plan.</p>
        </article>
      ) : null}
    </section>
  );
}
