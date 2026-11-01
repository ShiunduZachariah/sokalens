"use client";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";

import { buildDayWindow, formatDayTabLabel, resolveSelectedDay } from "../lib/dateWindow";
import { localePath, stripLocalePrefix } from "../i18n/config";
import { useLocale } from "./locale/LocaleProvider";
import { DEFAULT_VIEWER_TIMEZONE } from "../lib/viewerTimezone";

type DateTabsProps = {
  basePath?: string;
  timezone?: string;
  label?: string;
  /** "refreshed" uses the 2026-09-27 design's day strip. */
  variant?: "classic" | "refreshed";
};

const defaultTimezone = DEFAULT_VIEWER_TIMEZONE;

/**
 * `variant` opts a caller into the refreshed date strip.
 *
 * The default keeps the original markup because /results and the matches feed
 * still use it and have not been redesigned; restyling it globally would leave
 * them wearing one refreshed component on an otherwise untouched page.
 */
export function DateTabs({ basePath, timezone = defaultTimezone, label = "Match date", variant = "classic" }: DateTabsProps) {
  const refreshed = variant === "refreshed";
  const pathname = usePathname();
  const { locale } = useLocale();
  const searchParams = useSearchParams();
  const activePath = basePath ?? stripLocalePrefix(pathname);
  const now = new Date();
  const selectedDate = resolveSelectedDay(
    {
      date: searchParams.get("date") ?? undefined,
      day: searchParams.get("day") ?? undefined,
    },
    now,
    timezone,
  );
  const windowDays = buildDayWindow(now, timezone);

  return (
    <nav className={refreshed ? "td-strip" : "date-tabs"} aria-label={label}>
      {windowDays.map((day) => {
        const params = new URLSearchParams(searchParams.toString());
        params.set("date", day.isoDate);
        params.delete("day");
        const href = localePath(`${activePath}?${params.toString()}`, locale);
        return (
          <Link
            aria-current={day.isoDate === selectedDate ? (refreshed ? "page" : "date") : undefined}
            className={refreshed ? "td-day" : "date-tab"}
            href={href}
            key={day.isoDate}
            prefetch={false}
          >
            {refreshed ? (
              <>
                <span className="td-day__name">{formatDayTabLabel(day.isoDate, now, timezone)}</span>
                <span className="td-day__num">
                  {new Date(`${day.isoDate}T00:00:00Z`).toLocaleDateString(locale, {
                    day: "numeric",
                    timeZone: timezone,
                  })}
                </span>
              </>
            ) : (
              <>
                <strong>{formatDayTabLabel(day.isoDate, now, timezone)}</strong>
                <span>{new Date(`${day.isoDate}T00:00:00Z`).toLocaleDateString(locale, {
                  day: "numeric",
                  month: "short",
                  timeZone: timezone,
                })}</span>
              </>
            )}
          </Link>
        );
      })}
    </nav>
  );
}
