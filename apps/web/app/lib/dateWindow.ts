export type DayWindowItem = {
  isoDate: string;
  offsetDays: number;
};

type DaySelection = {
  date?: string;
  day?: string;
};

export function buildDayWindow(now: Date, timezone: string): DayWindowItem[] {
  const center = startOfLocalDay(now, timezone);
  // Today + the next 7 days (tomorrow and the weekend included); the past
  // days are intentionally removed from the navigation.
  return Array.from({ length: 8 }, (_, index) => {
    const offsetDays = index;
    const date = addDays(center, offsetDays, timezone);
    return {
      isoDate: formatIsoDate(date, timezone),
      offsetDays,
    };
  });
}

export function resolveSelectedDay(selection: DaySelection, now: Date, timezone: string) {
  if (selection.date?.trim()) {
    return selection.date;
  }
  const center = startOfLocalDay(now, timezone);
  switch (selection.day) {
    case "yesterday":
      return formatIsoDate(addDays(center, -1, timezone), timezone);
    case "tomorrow":
      return formatIsoDate(addDays(center, 1, timezone), timezone);
    case "today":
    case undefined:
    case "":
      return formatIsoDate(center, timezone);
    default:
      return formatIsoDate(center, timezone);
  }
}

/**
 * "Saturday 7 November", for a heading. Null when `isoDate` is not a calendar
 * date: `?date=` comes straight from the URL.
 *
 * Formatted in UTC because the value already names the day in the viewer's
 * zone, and `en-GB` because the design is day-first.
 */
export function formatLongDayLabel(isoDate: string, locale: string) {
  if (!/^\d{4}-\d{2}-\d{2}$/.test(isoDate)) return null;
  const anchored = new Date(`${isoDate}T12:00:00Z`);
  if (Number.isNaN(anchored.getTime())) return null;
  return new Intl.DateTimeFormat(locale === "en" ? "en-GB" : locale, {
    day: "numeric",
    month: "long",
    timeZone: "UTC",
    weekday: "long",
  }).format(anchored);
}

export function formatDayTabLabel(isoDate: string, now: Date, timezone: string) {
  const center = startOfLocalDay(now, timezone);
  const target = startOfLocalDay(new Date(`${isoDate}T00:00:00Z`), timezone);
  const offsetDays = dayDifference(center, target, timezone);

  if (offsetDays === 0) {
    return "Today";
  }

  const weekday = new Intl.DateTimeFormat("en", {
    timeZone: timezone,
    weekday: "short",
  }).format(target);

  if (offsetDays >= 7) {
    return `next ${weekday}`;
  }
  if (offsetDays <= -7) {
    return `last ${weekday}`;
  }
  return weekday;
}

function startOfLocalDay(value: Date, timezone: string) {
  const parts = new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).formatToParts(value);
  const year = Number(parts.find((part) => part.type === "year")?.value ?? "0");
  const month = Number(parts.find((part) => part.type === "month")?.value ?? "1");
  const day = Number(parts.find((part) => part.type === "day")?.value ?? "1");
  return new Date(Date.UTC(year, month - 1, day));
}

function addDays(value: Date, days: number, timezone: string) {
  const base = startOfLocalDay(value, timezone);
  return new Date(base.getTime() + days * 24 * 60 * 60 * 1000);
}

function formatIsoDate(value: Date, timezone: string) {
  return new Intl.DateTimeFormat("en-CA", {
    timeZone: timezone,
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
  }).format(value);
}

function dayDifference(left: Date, right: Date, timezone: string) {
  const leftDay = startOfLocalDay(left, timezone).getTime();
  const rightDay = startOfLocalDay(right, timezone).getTime();
  return Math.round((rightDay - leftDay) / (24 * 60 * 60 * 1000));
}
