"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { accountApiRequest } from "../../data/accountApi";
import { favoriteKey, normalizeFavoriteKeys } from "../../lib/favoriteKeys";
import { useAccount } from "./AccountContext";

type FavoritesContextValue = {
  /** True once the saved set reflects the server (or the viewer is a guest). */
  loaded: boolean;
  isFavorite: (entityType: string, entityId: string) => boolean;
  /** Records a local add/remove so every button for that entity agrees. */
  setFavorite: (entityType: string, entityId: string, saved: boolean) => void;
};

const FavoritesContext = createContext<FavoritesContextValue | null>(null);

export function FavoritesProvider({ children }: { children: ReactNode }) {
  const { access, status } = useAccount();
  const authenticated = Boolean(access?.authenticated);
  const [keys, setKeys] = useState<ReadonlySet<string>>(() => new Set<string>());
  const [loaded, setLoaded] = useState(false);

  useEffect(() => {
    if (status === "loading") return;
    if (!authenticated) {
      setKeys(new Set<string>());
      setLoaded(true);
      return;
    }
    let active = true;
    void accountApiRequest<unknown>("/v1/me/favorites?view=keys").then((result) => {
      if (!active) return;
      // A failed load must not be reported as "nothing is saved": that would
      // turn a saved heart back into an unsaved one, and the next click would
      // try to add something that is already there.
      if (result.ok) setKeys(new Set(normalizeFavoriteKeys(result.data)));
      setLoaded(true);
    });
    return () => {
      active = false;
    };
  }, [authenticated, status]);

  const setFavorite = useCallback((entityType: string, entityId: string, saved: boolean) => {
    const key = favoriteKey(entityType, entityId);
    setKeys((current) => {
      if (current.has(key) === saved) return current;
      const next = new Set(current);
      if (saved) next.add(key);
      else next.delete(key);
      return next;
    });
  }, []);

  const value = useMemo<FavoritesContextValue>(() => ({
    isFavorite: (entityType, entityId) => keys.has(favoriteKey(entityType, entityId)),
    loaded,
    setFavorite,
  }), [keys, loaded, setFavorite]);

  return <FavoritesContext.Provider value={value}>{children}</FavoritesContext.Provider>;
}

/**
 * Save buttons render in bulk -- /predictions/today draws one per country,
 * per league and per match -- so they all read one shared set instead of each
 * asking the server whether its own entity is saved.
 */
export function useFavorites() {
  const value = useContext(FavoritesContext);
  if (!value) throw new Error("useFavorites must be used within FavoritesProvider");
  return value;
}
