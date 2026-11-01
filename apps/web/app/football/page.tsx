import Link from "next/link";
import { OddsStrip } from "../components/OddsStrip";
import { ResponsibleNote } from "../components/ResponsibleNote";
import {
  fetchTodayMatches,
  matchScore,
  matchStatusLabel,
  predictionStateLabel,
  type LiveMatch,
} from "../data/liveMatches";
import {
  fetchTodayPredictions,
  matchLabel,
  marketLabel,
  marketSelectionLabel,
  type LivePrediction,
} from "../data/livePredictions";
import { formatConfidence } from "../lib/format";
import { canonicalPath } from "../site";
import { requestLocaleContext } from "../i18n/server";
import { fixtureHref } from "../lib/fixtureHref";

import { LocalTime } from "../components/LocalTime";
export const metadata = {
  title: "Football fixtures and predictions - SokaLens",
  description: "Football fixtures, predictions, leagues, teams, and odds context.",
  robots: { index: true, follow: true },
  alternates: {
    canonical: canonicalPath("/football"),
  },
};

export default async function FootballPage() {
  const { locale } = await requestLocaleContext();
  const [matchesResult, predictionsResult] = await Promise.allSettled([
    fetchTodayMatches(12),
    fetchTodayPredictions(3),
  ]);
  const liveMatches: LiveMatch[] = matchesResult.status === "fulfilled" ? matchesResult.value : [];
  const livePredictions: LivePrediction[] =
    predictionsResult.status === "fulfilled" ? predictionsResult.value : [];
  const matchesAvailable = matchesResult.status === "fulfilled";
  const predictionsAvailable = predictionsResult.status === "fulfilled";

  return (
    <main className="page-shell" id="content">
      <div className="main-column">
        <section>
          <p className="page-kicker">Football</p>
          <h1 className="page-title">Fixtures, predictions, and form signals.</h1>
          <p className="page-summary">
            A football-first dashboard for upcoming matches, league coverage, team form, and
            responsible model analysis.
          </p>
        </section>

        <section className="section" aria-labelledby="internal-fixtures">
          <div className="section-header">
            <div>
              <h2 id="internal-fixtures">Today&apos;s fixtures</h2>
              <p>Latest fixtures, kickoff times, match status, and prediction availability.</p>
            </div>
          </div>
          <div className="grid-three">
            {liveMatches.length > 0 ? (
              liveMatches.map((match) => (
                <article className="prediction-card" key={match.id}>
                  <div className="prediction-card__meta">
                    <span className="pill pill--pitch">{match.leagueName}</span>
                    <span className="pill">{matchStatusLabel(match)}</span>
                  </div>
                  <div>
                    <h3>
                      {match.homeTeam} vs {match.awayTeam}
                    </h3>
                    <p className="micro">
                      {matchScore(match) ?? <LocalTime timeOnly value={match.kickoffAt} />} - <LocalTime value={match.kickoffAt} />
                    </p>
                  </div>
                  <p className="muted">{predictionStateLabel(match.predictionState)}</p>
                  <OddsStrip odds={match.odds} />
                  <div className="prediction-card__actions">
                    <span className="micro">Match #{match.id}</span>
                    <Link className="button button--secondary" href={fixtureHref(match)}>View match</Link>
                  </div>
                </article>
              ))
            ) : (
              <article className="prediction-card">
                <h3>{matchesAvailable ? "No fixtures in today’s window" : "Fixture feed unavailable"}</h3>
                <p className="muted">
                  {matchesAvailable
                    ? "No fixtures are scheduled for the current Nairobi day."
                    : "Fixture updates are temporarily unavailable."}
                </p>
              </article>
            )}
          </div>
        </section>

        <section className="section" aria-labelledby="featured-predictions">
          <div className="section-header">
            <div>
              <h2 id="featured-predictions">Featured predictions</h2>
              <p>Market picks with confidence and price context.</p>
            </div>
            <a className="text-link" href="/predictions/today">
              View prediction feed
            </a>
          </div>
          <div className="grid-three">
            {livePredictions.length > 0 ? (
              livePredictions.map((prediction) => (
                <article className="prediction-card" key={prediction.id}>
                  <div className="prediction-card__meta">
                    <span className="pill pill--signal">{marketLabel(prediction.marketKey, locale)}</span>
                    <span className="pill">{formatConfidence(prediction.confidence)}</span>
                  </div>
                  <div>
                    <h3>{matchLabel(prediction)}</h3>
                    <p className="micro">
                      {prediction.leagueName || "Unknown league"} -{" "}
                      {prediction.kickoffAt ? <LocalTime value={prediction.kickoffAt} /> : "Kickoff pending"}
                    </p>
                    <p className="micro">
                      Version {prediction.publishedVersion} - <LocalTime value={prediction.publishedAt} />
                    </p>
                  </div>
                  <div className="prediction-card__pick">
                    <span className="micro">Model pick</span>
                    <strong>{marketSelectionLabel(prediction.marketKey, prediction.selection, locale)}</strong>
                  </div>
                  <p className="muted">Published SokaLens prediction with the latest available confidence.</p>
                  <div className="prediction-card__actions">
                    <span className="micro">Informational analysis</span>
                    <Link className="button button--secondary" href={fixtureHref(prediction)}>
                      View match
                    </Link>
                  </div>
                </article>
              ))
            ) : (
              <article className="prediction-card">
                <h3>{predictionsAvailable ? "No published predictions today" : "Live feed unavailable"}</h3>
                <p className="muted">
                  {predictionsAvailable
                    ? "No predictions have been published for today’s matches yet."
                    : "Featured predictions are temporarily unavailable."}
                </p>
              </article>
            )}
          </div>
        </section>

        <section className="section" aria-labelledby="live-stream-note">
          <h2 id="live-stream-note">How updates work</h2>
          <p className="muted">
            Fixture and prediction updates refresh throughout the day from prepared SokaLens data.
          </p>
        </section>
        <ResponsibleNote />
      </div>
    </main>
  );
}
