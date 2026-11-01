import { MatchesFeed } from "../../components/MatchesFeed";
import { ResponsibleNote } from "../../components/ResponsibleNote";
import { AdSlot } from "../../components/AdSlot";
import { fetchMatchCount, fetchMatches } from "../../data/liveMatches";
import { resolveSelectedDay } from "../../lib/dateWindow";
import { canonicalPath } from "../../site";
import { cookies } from "next/headers";
import { ViewerTimezone } from "../../components/ViewerTimezone";
import { validViewerTimezone } from "../../lib/viewerTimezone";
import Link from "next/link";

export const metadata = {
  title: "Today's football matches - SokaLens",
  description: "Today's football fixtures grouped by competition with prediction availability.",
  alternates: {
    canonical: canonicalPath("/matches/today"),
  },
};

type TodayMatchesPageProps = {
  searchParams?: Promise<Record<string, string | string[] | undefined>>;
};

function toStatusList(value: string | string[] | undefined) {
  if (Array.isArray(value)) {
    return value.filter((item) => item.length > 0);
  }
  if (typeof value === "string" && value.length > 0) {
    return [value];
  }
  return [];
}

function initialFilterFromStatuses(statuses: string[]) {
  if (statuses.some((status) => ["live", "halftime", "second_half", "extra_time", "penalties", "break"].includes(status))) {
    return "Live";
  }
  if (statuses.some((status) => ["upcoming", "delayed", "scheduled"].includes(status))) {
    return "Upcoming";
  }
  if (statuses.includes("finished")) {
    return "Finished";
  }
  return "All";
}

function positivePage(value: string | string[] | undefined) {
  const parsed = Number.parseInt(Array.isArray(value) ? value[0] ?? "" : value ?? "", 10);
  return Number.isInteger(parsed) && parsed > 0 ? parsed : 1;
}

function pageHref(params: Record<string, string | string[] | undefined>, page: number) {
  const query = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (key === "page") continue;
    for (const item of Array.isArray(value) ? value : [value]) if (item) query.append(key, item);
  }
  query.set("page", String(page));
  return `/matches/today?${query.toString()}`;
}

export default async function TodayMatchesPage({ searchParams }: TodayMatchesPageProps) {
  const resolvedSearchParams = (await searchParams) ?? {};
  const dateValue =
    typeof resolvedSearchParams.date === "string" ? resolvedSearchParams.date : undefined;
  const dayValue =
    typeof resolvedSearchParams.day === "string" ? resolvedSearchParams.day : undefined;
  const statuses = toStatusList(resolvedSearchParams.status);
  const page = positivePage(resolvedSearchParams.page);
  const pageSize = 20;
  const timezone = validViewerTimezone((await cookies()).get("sokalens_timezone")?.value);
  const selectedDate = resolveSelectedDay(
    { date: dateValue, day: dayValue },
    new Date(),
    timezone,
  );
  const matchQuery = {
    date: selectedDate,
    includeUnavailable: true,
    statuses,
    timezone,
  };
  const [matches, total] = await Promise.all([
    fetchMatches({ ...matchQuery, limit: pageSize, page }),
    fetchMatchCount(matchQuery),
  ]);
  const pageCount = Math.max(1, Math.ceil(total / pageSize));

  return (
    <main className="page-shell" id="content">
      <ViewerTimezone timezone={timezone} />
      <div className="content-grid">
        <div className="main-column">
          <section>
            <p className="page-kicker">Today&apos;s matches</p>
            <h1 className="page-title">Football fixtures by competition.</h1>
            <p className="page-summary">
              Scan kickoff times, live status, prediction availability, and odds freshness.
            </p>
          </section>

          <MatchesFeed
            initialFilter={initialFilterFromStatuses(statuses)}
            matches={matches}
            selectedDate={selectedDate}
            timezone={timezone}
          />
          {pageCount > 1 ? (
            <nav className="fixture-pagination" aria-label="Fixture pages">
              <Link aria-disabled={page <= 1} className="button button--secondary" href={pageHref(resolvedSearchParams, Math.max(1, page - 1))}>Previous fixtures</Link>
              <span><strong>{page}</strong><small>of {pageCount} · {total} fixtures</small></span>
              <Link aria-disabled={page >= pageCount} className="button button--secondary" href={pageHref(resolvedSearchParams, Math.min(pageCount, page + 1))}>Next fixtures</Link>
            </nav>
          ) : null}
        </div>

        <aside className="side-rail" aria-label="Match page support">
          <div className="rail-panel">
            <h2>Find the right fixture</h2>
            <p className="muted">
              Choose a date, then narrow the list by match status, prediction availability, or
              missing odds.
            </p>
          </div>
          <ResponsibleNote compact />
          <AdSlot format="sidebar" placement="page-rail" />
        </aside>
      </div>
    </main>
  );
}
