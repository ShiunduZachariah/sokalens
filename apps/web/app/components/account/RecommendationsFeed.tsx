"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  readBoolean,
  readNumber,
  readString,
  safeInternalHref,
  type RecommendationItem,
} from "../../data/accountApi";
import { EmptyState } from "../EmptyState";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

function normalizeRecommendations(payload: unknown): RecommendationItem[] {
  const root = asRecord(payload);
  const values = asArray(root.recommendations).length ? asArray(root.recommendations) : asArray(root.items).length ? asArray(root.items) : asArray(payload);
  return values.map((value) => {
    const item = asRecord(value);
    const content = asRecord(item.content);
    return {
      confidence: readNumber(item, "prediction_confidence", "confidence"),
      editorial: readBoolean(item, "editorial") ?? false,
      freshnessAt: readString(item, "freshness_at", "freshnessAt"),
      href: readString(item, "href", "url") ?? readString(content, "href", "url"),
      id: readString(item, "id", "recommendation_id") ?? "",
      itemType: readString(item, "item_type", "itemType", "type") ?? "match",
      reason: readString(item, "reason_text", "reason") ?? "Selected from your saved interests and current football coverage.",
      sponsored: readBoolean(item, "sponsored") ?? false,
      startsAt: readString(item, "starts_at", "startsAt"),
      subtitle: readString(item, "subtitle") ?? readString(content, "subtitle", "summary"),
      title: readString(item, "title", "name") ?? readString(content, "title", "name") ?? "Recommended match",
    };
  }).filter((item) => item.id);
}

function dateTime(value?: string) {
  if (!value) return undefined;
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

export function RecommendationsFeed({ onboardingComplete = false }: { onboardingComplete?: boolean }) {
  const { access, status } = useAccount();
  const [items, setItems] = useState<RecommendationItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();

  useEffect(() => {
    if (status === "loading") return;
    if (!access?.authenticated) {
      setLoading(false);
      return;
    }
    let active = true;
    void accountApiRequest<unknown>("/v1/recommendations?limit=24").then((result) => {
      if (!active) return;
      if (result.ok) setItems(normalizeRecommendations(result.data));
      else setError(result.message);
      setLoading(false);
    });
    return () => { active = false; };
  }, [access?.authenticated, status]);

  async function dismiss(item: RecommendationItem) {
    setBusy(item.id);
    setError(undefined);
    const result = await accountApiRequest<unknown>("/v1/recommendations/events", {
      body: JSON.stringify({ event_type: "dismissed", recommendation_id: item.id, reason: "not_interested" }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "POST",
    });
    setBusy(undefined);
    if (!result.ok) setError(result.message);
    else setItems((current) => current.filter((value) => value.id !== item.id));
  }

  if (loading || status === "loading") return <LoadingSkeleton count={5} label="Loading recommendations" variant="row" />;
  if (!access?.authenticated) {
    return <EmptyState actionHref="/login?next=/recommendations" actionLabel="Sign in" message="Recommendations use your explicit interests and saved football. Public predictions remain available without an account." title="Sign in for your feed" />;
  }

  return (
    <section className="section" aria-labelledby="recommendations-heading">
      {onboardingComplete ? <InlineStatus message="Preferences saved. Your feed will improve as prepared candidates become available." tone="success" /> : null}
      {error ? <InlineStatus message={error} tone="error" /> : null}
      <div className="section-header section-header--rich">
        <div>
          <h2 id="recommendations-heading">Recommended for you</h2>
          <p>Ranked from your interests and the latest prepared SokaLens match data.</p>
        </div>
        <Link className="button button--secondary" href="/onboarding">Tune feed</Link>
      </div>
      {items.length ? (
        <div className="recommendation-feed">
          {items.map((item) => (
            <article className="recommendation-card" key={item.id}>
              <div className="recommendation-card__meta">
                <span className="pill pill--signal">{item.itemType}</span>
                {item.editorial ? <span className="pill">Editorial</span> : null}
                {item.sponsored ? <span className="pill pill--gold">Sponsored</span> : null}
              </div>
              <div>
                <h3><Link href={safeInternalHref(item.href)}>{item.title}</Link></h3>
                {item.subtitle ? <p className="muted">{item.subtitle}</p> : null}
              </div>
              <p className="recommendation-card__reason"><span>Why this</span>{item.reason}</p>
              <div className="recommendation-card__footer">
                <span className="micro">
                  {item.confidence !== undefined ? `${Math.round(item.confidence)}% confidence` : "Prepared data"}
                  {dateTime(item.startsAt) ? ` · ${dateTime(item.startsAt)}` : ""}
                </span>
                <div className="button-row">
                  <button className="text-link text-link--button" disabled={busy === item.id} onClick={() => void dismiss(item)} type="button">Not interested</button>
                  <Link className="button button--secondary" href={safeInternalHref(item.href)}>Open</Link>
                </div>
              </div>
            </article>
          ))}
        </div>
      ) : <EmptyState actionHref="/onboarding" actionLabel="Choose interests" message="Your recommendation feed is empty. Choose interests or save a team, league, or match to help rank prepared content." title="No recommendations yet" />}
    </section>
  );
}
