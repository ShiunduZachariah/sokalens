"use client";

import Link from "next/link";
import { useEffect, useState } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  readNumber,
  readString,
  safeInternalHref,
  type FavoriteItem,
} from "../../data/accountApi";

import { matchStatusLabel } from "../../lib/matchStatus";
import { EmptyState } from "../EmptyState";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { useFavorites } from "./FavoritesContext";
import { InlineStatus } from "./InlineStatus";
import { fixtureHref } from "../../lib/fixtureHref";

import { LocalTime } from "../LocalTime";
function normalizeFavorites(payload: unknown): FavoriteItem[] {
  const root = asRecord(payload);
  const values = asArray(root.favorites).length ? asArray(root.favorites) : asArray(root.items).length ? asArray(root.items) : asArray(payload);
  return values.map((value) => {
    const item = asRecord(value);
    const entity = asRecord(item.entity);
    const entityType = readString(item, "entity_type", "entityType", "type") ?? readString(entity, "type") ?? "entity";
    const entityId = readString(item, "entity_id", "entityId") ?? readString(entity, "id") ?? "";
    const matches = asArray(item.matches).map((value) => {
      const match = asRecord(value);
      const id = readString(match, "id") ?? "";
      return {
        awayScore: readNumber(match, "away_score", "awayScore"),
        awayTeam: readString(match, "away_team", "awayTeam") ?? "Away",
        homeScore: readNumber(match, "home_score", "homeScore"),
        homeTeam: readString(match, "home_team", "homeTeam") ?? "Home",
        href: safeInternalHref(
          readString(match, "href"),
          id ? fixtureHref({ id, leagueName: readString(match, "league_name", "leagueName"), homeTeam: readString(match, "home_team", "homeTeam"), awayTeam: readString(match, "away_team", "awayTeam") }) : "/matches/today",
        ),
        id,
        kickoffAt: readString(match, "kickoff_at", "kickoffAt") ?? "",
        leagueName: readString(match, "league_name", "leagueName"),
        status: readString(match, "status") ?? "scheduled",
      };
    }).filter((match) => match.id);
    return {
      contextLabel: readString(item, "context_label", "contextLabel"),
      entityId,
      entityType,
      href: readString(item, "href", "url") ?? readString(entity, "href", "url"),
      id: readString(item, "id", "favorite_id") ?? `${entityType}:${entityId}`,
      matches,
      name: readString(item, "name", "title") ?? readString(entity, "name", "title") ?? "Saved item",
      subtitle: readString(item, "subtitle", "description") ?? readString(entity, "subtitle", "description"),
    };
  }).filter((item) => item.entityId);
}

function FavoriteMatchRow({ item }: { item: NonNullable<FavoriteItem["matches"]>[number] }) {
  const hasScore = item.homeScore !== undefined && item.awayScore !== undefined;
  return (
    <Link className="favorite-match" href={item.href}>
      <span className="favorite-match__teams">
        <strong>{item.homeTeam}</strong>
        <span aria-hidden="true">–</span>
        <strong>{item.awayTeam}</strong>
      </span>
      <span className="favorite-match__meta">
        {hasScore ? <strong>{item.homeScore}–{item.awayScore}</strong> : item.kickoffAt ? <span><LocalTime value={item.kickoffAt} /></span> : null}
        <span>{matchStatusLabel(item.status)}</span>
      </span>
    </Link>
  );
}

export function FavoritesList() {
  const { access, status } = useAccount();
  const { setFavorite } = useFavorites();
  const [items, setItems] = useState<FavoriteItem[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();

  useEffect(() => {
    if (status === "loading") return;
    if (!access?.authenticated) {
      setLoading(false);
      return;
    }
    let active = true;
    void accountApiRequest<unknown>("/v1/me/favorites?limit=50").then((result) => {
      if (!active) return;
      if (result.ok) setItems(normalizeFavorites(result.data));
      else setError(result.message);
      setLoading(false);
    });
    return () => { active = false; };
  }, [access?.authenticated, status]);

  async function removeFavorite(item: FavoriteItem) {
    setBusy(item.id);
    setError(undefined);
    setMessage(undefined);
    const result = await accountApiRequest<unknown>("/v1/me/favorites", {
      body: JSON.stringify({ entity_id: item.entityId, entity_type: item.entityType }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "DELETE",
    });
    setBusy(undefined);
    if (!result.ok) setError(result.message);
    else {
      // Keep the shared saved set in step so a save button elsewhere on the
      // site does not keep showing this entity as saved.
      setFavorite(item.entityType, item.entityId, false);
      setItems((current) => current.filter((value) => value.id !== item.id));
      setMessage(`${item.name} removed from favorites.`);
    }
  }

  if (loading || status === "loading") return <LoadingSkeleton count={3} label="Loading favorites" variant="card" />;
  if (!access?.authenticated) {
    return (
      <EmptyState
        actionHref="/login?next=/favorites"
        actionLabel="Sign in"
        message="Create or sign in to keep countries, competitions, teams, matches, and markets together across devices."
        title="Sign in to save favorites"
      />
    );
  }

  return (
    <section className="section" aria-labelledby="favorites-heading">
      <div className="section-header section-header--rich">
        <div>
          <h2 id="favorites-heading">Saved football</h2>
          <p>{items.length} saved {items.length === 1 ? "item" : "items"}</p>
        </div>
        <Link className="button button--secondary" href="/leagues">Find competitions</Link>
      </div>
      {message ? <InlineStatus message={message} tone="success" /> : null}
      {error ? <InlineStatus message={error} tone="error" /> : null}
      {items.length ? (
        <div className="favorite-grid">
          {items.map((item) => (
            <article className="favorite-card" key={item.id}>
              <div className="favorite-card__copy">
                <span className="pill">{item.entityType}</span>
                <h3><Link href={safeInternalHref(item.href)}>{item.name}</Link></h3>
                {item.subtitle ? <p className="muted">{item.subtitle}</p> : null}
                {item.matches?.length ? (
                  <div className="favorite-card__matches">
                    <span className="micro muted">{item.contextLabel ?? "Upcoming"}</span>
                    {item.matches.map((match) => <FavoriteMatchRow item={match} key={match.id} />)}
                  </div>
                ) : null}
              </div>
              <div className="favorite-card__actions">
                <Link className="text-link" href={safeInternalHref(item.href)}>Open</Link>
                <button className="text-link text-link--button text-link--danger" disabled={busy === item.id} onClick={() => void removeFavorite(item)} type="button">
                  {busy === item.id ? "Updating…" : "Remove"}
                </button>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <EmptyState
          actionHref="/matches/today"
          actionLabel="Browse today's fixtures"
          message="Use favorite controls on countries, competitions, teams, matches, and markets to build this list."
          title="Nothing saved yet"
        />
      )}
    </section>
  );
}
