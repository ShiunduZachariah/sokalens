import Link from "next/link";
import type { ReactNode } from "react";
import { DateTabs } from "../../components/DateTabs";
import type { PredictionExplorerItem } from "../../components/PredictionExplorer";
import {
  fetchAllTodayPredictionsWithProvider,
  type LivePrediction,
  type ProviderLivePrediction,
} from "../../data/livePredictions";

import { formatLongDayLabel, resolveSelectedDay } from "../../lib/dateWindow";
import { localeAlternates, localePath } from "../../i18n/config";
import { translate } from "../../i18n/catalogs";
import { requestLocaleContext } from "../../i18n/server";
import { fetchMatchCount } from "../../data/liveMatches";
import { cookies } from "next/headers";
import { validViewerTimezone } from "../../lib/viewerTimezone";
import { PredictionListShell } from "../../components/PredictionListShell";
import { fromLivePrediction, fromProviderLivePrediction } from "../../data/predictionItems";

/**
 * The name of the day the strip has moved this page to, or null when it is
 * still showing today. This URL serves all eight days, and the heading has to
 * follow: "Today's football predictions" above Sunday's fixtures is simply wrong.
 */
function otherDayLabel(selectedDate: string, timezone: string, locale: string) {
  const today = resolveSelectedDay({}, new Date(), timezone);
  return selectedDate === today ? null : formatLongDayLabel(selectedDate, locale);
}

export async function generateMetadata({ searchParams }: Pick<TodayPredictionsPageProps, "searchParams">) {
  const { locale, publicPath } = await requestLocaleContext();
  const dateValue = (await searchParams)?.date;
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const date = typeof dateValue === "string"
    ? otherDayLabel(resolveSelectedDay({ date: dateValue }, new Date(), timezone), timezone, locale)
    : null;
  return {
    title: date ? translate(locale, "day.metadata_title", { date }) : translate(locale, "today.metadata_title"),
    description: date ? translate(locale, "day.metadata_description", { date }) : translate(locale, "today.metadata_description"),
    alternates: { canonical: publicPath, languages: localeAlternates(publicPath) },
  };
}

type TodayPredictionsPageProps = {
  searchParams?: Promise<Record<string, string | string[] | undefined>>;
  forcedDay?: "today" | "tomorrow";
  hubLabel?: string;
  /**
   * Rendered directly under the page intro, inside `<main id="content">`.
   *
   * /predictions/tomorrow leads with its ten highest-confidence picks, and
   * they have to sit inside the content landmark: rendered around this
   * component instead, they would fall outside `#content` and the skip link
   * would jump a reader straight past the only thing the page is for.
   */
  leadContent?: ReactNode;
};

export default async function TodayPredictionsPage({ searchParams, forcedDay, hubLabel, leadContent }: TodayPredictionsPageProps) {
  const { locale } = await requestLocaleContext();
  const resolvedSearchParams = (await searchParams) ?? {};
  const dateValue =
    typeof resolvedSearchParams.date === "string" ? resolvedSearchParams.date : undefined;
  const dayValue =
    typeof resolvedSearchParams.day === "string" ? resolvedSearchParams.day : forcedDay;
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const selectedDate = resolveSelectedDay(
    { date: dateValue, day: dayValue },
    new Date(),
    timezone,
  );

  const dayLabel = forcedDay ? null : otherDayLabel(selectedDate, timezone, locale);

  let livePredictions: LivePrediction[] = [];
  let providerPredictions: ProviderLivePrediction[] = [];
  let totalFixtures = 0;
  let feedState: "live" | "empty" | "unavailable" = "unavailable";
  const [fixtureCountResult, feedResult] = await Promise.allSettled([
    fetchMatchCount({ date: selectedDate, timezone, includeUnavailable: true }),
    fetchAllTodayPredictionsWithProvider(selectedDate, timezone),
  ]);
  if (fixtureCountResult.status === "fulfilled") totalFixtures = fixtureCountResult.value;
  if (feedResult.status === "fulfilled") {
    const feed = feedResult.value;
    livePredictions = feed.engine;
    providerPredictions = feed.provider;
    feedState = livePredictions.length > 0 || providerPredictions.length > 0 ? "live" : "empty";
  } else {
    feedState = "unavailable";
  }

  // One unified feed: engine predictions first, provider fallbacks merged in,
  // deterministic kickoff ordering, no source labels on the public site.
  //
  // Deduplicated on matchId, which is the only key both sources share. It
  // used to key on `item.id`, and that let the same match through twice: an
  // engine item carries the prediction's numeric id while a provider item
  // carries the string `provider-<matchId>`, and the filter only acted when
  // the id was a number. Provider fallbacks were therefore never deduplicated
  // against anything, so a match with both an engine prediction and a
  // fallback rendered as two cards for one game.
  const seen = new Set<number>();
  const merged = [
    ...livePredictions.map((prediction) => fromLivePrediction(prediction, locale, timezone)),
    ...providerPredictions.map((prediction) => fromProviderLivePrediction(prediction, locale, timezone)),
  ].filter((item) => {
    // Engine items come first in the array above, so on a collision the
    // engine prediction is the one kept and the provider fallback drops --
    // which is the right way round.
    if (typeof item.matchId !== "number") {
      return true;
    }
    if (seen.has(item.matchId)) {
      return false;
    }
    seen.add(item.matchId);
    return true;
  });
  const items: PredictionExplorerItem[] = merged.sort((left, right) => {
    const leftKickoff = Date.parse(left.kickoffAt ?? "");
    const rightKickoff = Date.parse(right.kickoffAt ?? "");
    if (!Number.isNaN(leftKickoff) && !Number.isNaN(rightKickoff) && leftKickoff !== rightKickoff) {
      return leftKickoff - rightKickoff;
    }
    return String(left.match).localeCompare(String(right.match));
  });

  const emptyState = (
    <div className="ac-empty" aria-live="polite">
      <h3>
        {feedState === "empty"
          ? `No published predictions for ${dayLabel ?? "today"}`
          : "The prediction feed is temporarily unavailable"}
      </h3>
      <p>
        {feedState === "empty"
          ? `${dayLabel ? "The" : "Today\u2019s"} prediction list is up to date, but no signals fall inside the current match window. Sample picks are not substituted for a quiet slate.`
          : `${dayLabel ? "These" : "Today\u2019s"} predictions could not be loaded. Try again shortly; no sample picks are being shown as current data.`}
      </p>
      <Link className="mp-btn" href={localePath("/matches/today", locale)}>
        Browse today&apos;s fixtures
      </Link>
    </div>
  );

  return (
    <PredictionListShell
      breadcrumb={hubLabel ?? translate(locale, "nav.predictions")}
      dateStrip={
        forcedDay ? null : (
          <DateTabs
            basePath="/predictions/today"
            label="Prediction date"
            timezone={timezone}
            variant="refreshed"
          />
        )
      }
      emptyState={emptyState}
      eyebrow={
        feedState === "live"
          ? "Live prediction feed"
          : feedState === "empty"
            ? "Live feed \u00b7 no signals"
            : "Prediction feed unavailable"
      }
      items={items}
      leadContent={leadContent}
      locale={locale}
      /*
        The "Markets" figure comes from the shell, because the shell is what
        packs the market labels and the two have to agree. "Predictions" is
        counted from the deduplicated feed rather than the API's `total`: the
        feed total counts rows the matches endpoint excludes -- `unknown` and
        `postponed` -- and was never deduplicated, so on 2026-09-21 it read 205
        predicted against 200 total fixtures, a number that cannot exist.
        `merged` is the set actually rendered, so it cannot disagree with what a
        reader can count.
      */
      marketCountStat={(count) => ({ label: "Markets", value: count })}
      stats={[
        { label: "Fixtures", value: totalFixtures },
        { label: "Predictions", value: merged.length, accent: true },
      ]}
      subnavNoCurrent={dayLabel !== null}
      summary={dayLabel ? translate(locale, "day.summary", { date: dayLabel }) : translate(locale, forcedDay === "tomorrow" ? "tomorrow.summary" : "today.summary")}
      title={dayLabel ? translate(locale, "day.title", { date: dayLabel }) : translate(locale, forcedDay === "tomorrow" ? "tomorrow.title" : "today.title")}
    />
  );
}
