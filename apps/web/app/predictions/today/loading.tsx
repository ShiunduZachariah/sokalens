import { LoadingSkeleton } from "../../components/LoadingSkeleton";

export default function PredictionsLoading() {
  return (
    <main className="page-shell" id="content">
      <section className="page-intro">
        <p className="page-kicker">Today&apos;s predictions</p>
        <div className="page-title" role="status" aria-live="polite">Loading the prediction slate</div>
        <p className="page-summary">Preparing published selections and confidence levels.</p>
      </section>
      <LoadingSkeleton count={6} label="Loading today's predictions" variant="row" />
    </main>
  );
}
