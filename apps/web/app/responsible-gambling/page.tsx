import { ResponsibleNote } from "../components/ResponsibleNote";
import { canonicalPath } from "../site";

export const metadata = {
  title: "Responsible gambling - SokaLens",
  description: "Responsible use guidance for SokaLens football predictions and odds context.",
  alternates: {
    canonical: canonicalPath("/responsible-gambling"),
  },
};

export default function ResponsibleGamblingPage() {
  return (
    <main className="page-shell" id="content">
      <div className="main-column">
        <article className="legal-panel">
          <p className="page-kicker">Responsible gambling</p>
          <h1 className="page-title">Use SokaLens as analysis, not financial or betting advice.</h1>
          <p>
            SokaLens provides football match intelligence, model confidence, and odds context for
            informational purposes only. Nothing on the site is financial or betting advice, and
            no prediction is a promise of an outcome. Match outcomes remain uncertain.
          </p>
          <h2>Healthy use principles</h2>
          <ul>
            <li>Set personal limits before using prediction or odds information.</li>
            <li>Do not chase losses or treat model confidence as certainty.</li>
            <li>Take breaks when football analysis stops feeling recreational.</li>
            <li>Seek local support services if gambling is causing harm.</li>
          </ul>
          <h2>Restricted access</h2>
          <p>
            Location, age, advertising, and legal restrictions are enforced through server-side
            policy controls and audited restriction rules, so the site can comply with the rules
            that apply to each visitor.
          </p>
        </article>
        <ResponsibleNote />
      </div>
    </main>
  );
}
