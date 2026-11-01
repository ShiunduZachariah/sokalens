import { LoadingSkeleton } from "../../components/LoadingSkeleton";

export default function MatchesLoading() {
  return (
    <main className="page-shell" id="content">
      <section className="page-intro">
        <p className="page-kicker">Today&apos;s fixtures</p>
        <div className="page-title" role="status" aria-live="polite">Loading the fixture schedule</div>
        <p className="page-summary">Preparing kickoff times, match status, and prediction availability.</p>
      </section>
      <LoadingSkeleton count={6} label="Loading today's fixtures" variant="row" />
    </main>
  );
}
