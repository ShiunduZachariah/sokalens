import { canonicalPath } from "../site";

export const metadata = {
  title: "About SokaLens - Football predictions, explained",
  description: "How SokaLens uses match data and statistical models, what confidence means, and how to check results or report a correction.",
  alternates: { canonical: canonicalPath("/about") },
};

export default function AboutPage() {
  return (
    <main className="page-shell" id="content">
      <div className="main-column prose-page">
        <section className="page-intro">
          <p className="page-kicker">About SokaLens</p>
          <h1 className="page-title">Football predictions with the reasoning kept in view.</h1>
          <p className="page-summary">
            SokaLens is focused on making football prediction signals easier to understand,
            compare, and revisit after the final whistle.
          </p>
        </section>
        <section className="section">
          <h2>How the analysis works</h2>
          <p>
            SokaLens combines fixture information, available team history and statistical
            models to estimate football outcomes. Our goal models use scoring probabilities
            to compare match results, goal totals and both-teams-to-score selections.
            Coverage varies by competition: lineups, events and detailed statistics are
            not available for every match.
          </p>
          <h2>Reading a prediction</h2>
          <p>
            Confidence is a model estimate, not a promise. A high-confidence selection can
            lose, and a successful prediction does not establish that the model is reliable.
            Compare the selected market and goal line, the available match context and a
            sufficiently large record of settled results. Missing information and delayed
            provider updates can limit what we can show.
          </p>
          <p>
            Start with <a href="/predictions/tomorrow">tomorrow&apos;s fixtures</a>, explore
            <a href="/predictions/markets/over-2-5-goals"> Over 2.5 goals</a>, or review
            <a href="/results"> published results</a> before drawing conclusions.
          </p>
          <h2>Results and corrections</h2>
          <p>
            Predictions are compared with recorded match outcomes after the game. A missing
            result is not a loss, and unavailable statistics must not be guessed. Data and
            software errors can occur; when reporting one, include the match link, market,
            selection and the result you believe is incorrect so we can trace the record.
          </p>
          <h2>Still building and evolving</h2>
          <p>
            The product continues to grow as coverage, models, and match context improve. Every
            prediction is presented as a statistical estimate and should never be treated as certain.
          </p>
          <h2>Comments and suggestions</h2>
          <p>
            Found something confusing, missing, or worth improving? Send your feedback to{" "}
            <a href="mailto:support@sokalens.com?subject=SokaLens%20feedback">support@sokalens.com</a>.
          </p>
          <a className="button" href="mailto:support@sokalens.com?subject=SokaLens%20feedback">Send feedback</a>
        </section>
      </div>
    </main>
  );
}
