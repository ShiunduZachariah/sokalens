export function validCatalogSlug(value: unknown): value is string {
  if (typeof value !== "string") return false;
  return /^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(value) && !["null", "undefined", "nan"].includes(value);
}

export function validEntityId(value: unknown): value is number {
  return typeof value === "number" && Number.isSafeInteger(value) && value > 0;
}

export function validRouteText(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0;
}

export function teamPath(value: unknown) {
  return validCatalogSlug(value) ? `/teams/${value}` : null;
}

export function leaguePredictionPath(value: unknown) {
  return validCatalogSlug(value) ? `/predictions/leagues/${value}` : null;
}

export function matchPath(value: unknown) {
  if ((typeof value !== "number" && typeof value !== "string") || value === "") return null;
  const id = Number(value);
  return validEntityId(id) ? `/matches/${id}` : null;
}
