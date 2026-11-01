import Link from "next/link";
import { cookies } from "next/headers";
import { fetchAllTodayPredictionsWithProvider } from "../../data/livePredictions";
import { fromLivePrediction, fromProviderLivePrediction } from "../../data/predictionItems";
import { buildDayWindow } from "../../lib/dateWindow";
import { validViewerTimezone } from "../../lib/viewerTimezone";
import { canonicalPath } from "../../site";
import { requestLocaleContext } from "../../i18n/server";
import { localePath } from "../../i18n/config";
import { PredictionListShell } from "../../components/PredictionListShell";
import type { PredictionExplorerItem } from "../../components/PredictionExplorer";

export const metadata = {
  title: "Weekend football predictions - SokaLens",
  description: "Published Saturday and Sunday football predictions with league, kickoff, market, and confidence details.",
  alternates: { canonical: canonicalPath("/predictions/weekend") },
};

function weekendDates(now: Date, timezone: string) {
  const window = buildDayWindow(now, timezone);
  const candidates = window.filter(({ isoDate }) => {
    const weekday = new Intl.DateTimeFormat("en", { timeZone: timezone, weekday: "short" }).format(new Date(`${isoDate}T12:00:00Z`));
    return weekday === "Sat" || weekday === "Sun";
  });
  if (candidates.length === 0) return [];
  const firstWeekday = new Intl.DateTimeFormat("en", { timeZone: timezone, weekday: "short" }).format(new Date(`${candidates[0].isoDate}T12:00:00Z`));
  return candidates.slice(0, firstWeekday === "Sun" ? 1 : 2);
}

export default async function WeekendPredictionsPage() {
  const { locale } = await requestLocaleContext();
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const days = weekendDates(new Date(), timezone);
  const feeds = await Promise.all(days.map(async (day) => ({
    isoDate: day.isoDate,
    feed: await fetchAllTodayPredictionsWithProvider(day.isoDate, timezone).catch(() => ({ engine: [], provider: [], total: 0 })),
  })));

  const dayLabel = (isoDate: string) =>
    new Intl.DateTimeFormat("en-GB", { timeZone: timezone, weekday: "short", day: "numeric", month: "short" })
      .format(new Date(`${isoDate}T12:00:00Z`));

  /*
   * Both days in one feed, each row tagged with its day.
   *
   * The day tag is what drives the design's Weekend grouping: the shell's
   * explorer groups by day when the slate spans more than one, and that is the
   * only thing distinguishing this page from Tomorrow. It also populates the
   * explorer's date filter, so Saturday-only is one select away.
   *
   * Deduplicated on matchId across the whole weekend rather than per day, and
   * engine rows are listed before provider fallbacks so a collision keeps the
   * engine prediction -- the same rule the Today page applies, for the same
   * reason: a match with both sources used to render as two rows for one game.
   */
  const seen = new Set<number>();
  const items: PredictionExplorerItem[] = feeds
    .flatMap(({ isoDate, feed }) => {
      const date = dayLabel(isoDate);
      return [
        ...feed.engine.map((prediction) => ({ ...fromLivePrediction(prediction, locale), date })),
        ...feed.provider.map((prediction) => ({ ...fromProviderLivePrediction(prediction, locale), date })),
      ];
    })
    .filter((item) => {
      if (typeof item.matchId !== "number") return true;
      if (seen.has(item.matchId)) return false;
      seen.add(item.matchId);
      return true;
    })
    .sort((left, right) => {
      const leftKickoff = Date.parse(left.kickoffAt ?? "");
      const rightKickoff = Date.parse(right.kickoffAt ?? "");
      if (!Number.isNaN(leftKickoff) && !Number.isNaN(rightKickoff) && leftKickoff !== rightKickoff) {
        return leftKickoff - rightKickoff;
      }
      return String(left.match).localeCompare(String(right.match));
    });

  const span = days.length > 0
    ? days.map((day) => dayLabel(day.isoDate)).join(" – ")
    : "This weekend";

  return (
    <PredictionListShell
      accent="predictions"
      breadcrumb="Weekend"
      emptyState={
        <div className="ac-empty" aria-live="polite">
          <h3>No published weekend selections yet</h3>
          <p>
            New predictions appear here automatically when the engine publishes them. Nothing is
            substituted for a slate the model has not covered yet.
          </p>
          <Link className="mp-btn" href={localePath("/predictions/today", locale)}>
            Today&apos;s predictions
          </Link>
        </div>
      }
      eyebrow={`${span} · ${timezone}`}
      items={items}
      locale={locale}
      marketCountStat={(count) => ({ label: "Markets", value: count })}
      stats={[
        { label: "Matches", value: items.length, accent: true },
        { label: "Competitions", value: new Set(items.map((item) => item.league)).size },
      ]}
      summary="Every weekend match with a published pick, grouped by day and competition."
      title="Weekend football"
    />
  );
}
