"use client";

import Link from "next/link";
import { useState } from "react";
import { accountApiRequest } from "../../data/accountApi";
import { useAccount } from "./AccountContext";
import { favoriteMutation } from "../../lib/favoriteKeys";
import { useFavorites } from "./FavoritesContext";

type FavoriteButtonProps = {
  compact?: boolean;
  /**
   * League sub-headers get a 36x36 hit area against a row's 44x44 (review
   * §2.2). Only meaningful with `compact`, which is the icon-only ghost.
   */
  subheader?: boolean;
  entityId: string;
  entityType: "competition" | "country" | "league" | "market" | "match" | "prediction" | "team";
  hideForGuests?: boolean;
  label: string;
  returnPath: string;
};

function HeartIcon({ filled }: { filled: boolean }) {
  return (
    <svg aria-hidden="true" className="favorite-control__icon" fill={filled ? "currentColor" : "none"} height="18" viewBox="0 0 24 24" width="18">
      <path d="M20.8 4.7a5.5 5.5 0 0 0-7.8 0L12 5.8l-1-1.1a5.5 5.5 0 1 0-7.8 7.8L12 21l8.8-8.5a5.5 5.5 0 0 0 0-7.8Z" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.9" />
    </svg>
  );
}

export function FavoriteButton({ compact = false, entityId, entityType, hideForGuests = false, label, returnPath, subheader = false }: FavoriteButtonProps) {
  const { access, status } = useAccount();
  const { isFavorite, loaded, setFavorite } = useFavorites();
  const [pending, setPending] = useState<"remove" | "save">();
  const [error, setError] = useState<string>();

  // Saved state is read from the shared set, not from local state seeded to
  // false. That is what makes the control correct on first paint and after a
  // reload, and what keeps two buttons for the same entity in agreement.
  const saved = isFavorite(entityType, entityId);

  if (hideForGuests && status !== "ready") return null;
  if (status === "ready" && !access?.authenticated) {
    if (hideForGuests) return null;
    return <Link className="button button--secondary button--small" href={`/login?next=${encodeURIComponent(returnPath)}`}>Sign in to save</Link>;
  }

  async function toggle() {
    const next = !saved;
    setPending(next ? "save" : "remove");
    setError(undefined);
    // Optimistic: flip first, roll back if the request fails.
    setFavorite(entityType, entityId, next);
    const { body, method } = favoriteMutation(entityType, entityId, label, next);
    const result = await accountApiRequest<unknown>("/v1/me/favorites", {
      body,
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method,
    });
    setPending(undefined);
    if (!result.ok) {
      setFavorite(entityType, entityId, !next);
      setError(result.message);
    }
  }

  // `pending` carries the direction of the in-flight request, because `saved`
  // has already been flipped optimistically by the time this renders.
  const busy = pending !== undefined;
  // Review §2.2 fixes the wording: a league is followed, a match is favourited.
  const actionLabel = saved
    ? entityType === "league"
      ? `Unfollow ${label}`
      : `Remove ${label} from favourites`
    : entityType === "league"
      ? `Follow ${label}`
      : `Add ${label} to favourites`;
  const busyText = pending === "remove" ? "Removing…" : "Saving…";
  const buttonText = busy ? busyText : saved ? "Saved" : `Save ${entityType}`;

  return (
    <span className="favorite-control">
      <button
        aria-label={actionLabel}
        aria-pressed={saved}
        className={
          compact
            // Ghost: deliberately NONE of the `.button` classes. Those carry the
            // border and ground that UI-03 is about, and overriding them from a
            // modifier means every future `.button` change can put the box back.
            ? `favorite-control__heart${subheader ? " favorite-control__heart--subheader" : ""}`
            : saved
              ? "button button--small favorite-control__button favorite-control__button--saved"
              : "button button--secondary button--small favorite-control__button"
        }
        disabled={busy || status !== "ready" || !loaded}
        onClick={() => void toggle()}
        title={actionLabel}
        type="button"
      >
        <HeartIcon filled={saved} />
        {compact ? <span className="sr-only">{busy ? busyText : saved ? "Saved" : `Save ${label}`}</span> : buttonText}
      </button>
      {error ? <span className="favorite-control__error" role="alert">{error}</span> : null}
    </span>
  );
}
