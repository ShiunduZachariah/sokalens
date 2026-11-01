import { LoadingSkeleton } from "../components/LoadingSkeleton";

export default function ResultsLoading() {
  return (
    <main className="page-shell" id="content">
      <section className="page-intro">
        <p className="page-kicker">Accuracy and results</p>
        <div className="page-title" role="status" aria-live="polite">Loading performance data</div>
        <p className="page-summary">Preparing settled predictions and market hit rates.</p>
      </section>
      <LoadingSkeleton count={1} label="Loading accuracy summary" variant="chart" />
      <LoadingSkeleton count={5} label="Loading market results" variant="row" />
    </main>
  );
}
