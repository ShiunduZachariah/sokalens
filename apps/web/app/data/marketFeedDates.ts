/** Calendar dates in the viewer's timezone, without DST-dependent 24-hour shifts. */
export function marketFeedDates(startDate: string | undefined, timezone: string, days: number, now = new Date()): string[] {
  let first = startDate;
  if (!first) {
    const parts = new Intl.DateTimeFormat("en", {
      timeZone: timezone,
      year: "numeric",
      month: "2-digit",
      day: "2-digit",
    }).formatToParts(now);
    const part = (type: Intl.DateTimeFormatPartTypes) => parts.find((item) => item.type === type)?.value ?? "";
    first = `${part("year")}-${part("month")}-${part("day")}`;
  }
  const [year, month, day] = first.split("-").map(Number);
  const midnight = Date.UTC(year, month - 1, day);
  return Array.from({ length: Math.min(7, Math.max(1, Math.floor(days))) }, (_, offset) =>
    new Date(midnight + offset * 86_400_000).toISOString().slice(0, 10),
  );
}
