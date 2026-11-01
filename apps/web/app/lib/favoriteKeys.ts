import { asArray, asRecord, readString } from "../data/accountApi.ts";

/**
 * The key a saved entity is stored under.
 *
 * The API lowercases `entity_type` and `entity_id` on both write and delete
 * (accounts.Service.AddFavorite / RemoveFavorite), so anything comparing a
 * component's props against what the server returned has to lowercase too.
 * Compare a raw `entityId` -- a mixed-case team slug, say -- and every
 * already-saved entity reads back as unsaved, which looks exactly like the
 * save button being one-way.
 */
export function favoriteKey(entityType: string, entityId: string | number) {
  return `${String(entityType).trim().toLowerCase()}:${String(entityId).trim().toLowerCase()}`;
}

/** Saved keys from `GET /v1/me/favorites?view=keys` (or the full list form). */
export function normalizeFavoriteKeys(payload: unknown): string[] {
  const root = asRecord(payload);
  const values = asArray(root.favorites).length
    ? asArray(root.favorites)
    : asArray(root.items).length
      ? asArray(root.items)
      : asArray(payload);
  return values
    .map((value) => {
      const item = asRecord(value);
      const entity = asRecord(item.entity);
      const entityType = readString(item, "entity_type", "entityType", "type") ?? readString(entity, "type");
      const entityId = readString(item, "entity_id", "entityId") ?? readString(entity, "id");
      return entityType && entityId ? favoriteKey(entityType, entityId) : undefined;
    })
    .filter((key): key is string => Boolean(key));
}

export type FavoriteMutation = {
  body: string;
  method: "DELETE" | "POST";
};

/**
 * The request that moves a favorite to `next`.
 *
 * Removal was always available on the API -- `DELETE /v1/me/favorites` is what
 * the /favorites page has always called -- the save button simply never
 * issued it, which is why unsaving meant going to settings.
 */
export function favoriteMutation(
  entityType: string,
  entityId: string,
  label: string,
  next: boolean,
): FavoriteMutation {
  return next
    ? { body: JSON.stringify({ entity_id: entityId, entity_type: entityType, name: label }), method: "POST" }
    : { body: JSON.stringify({ entity_id: entityId, entity_type: entityType }), method: "DELETE" };
}
