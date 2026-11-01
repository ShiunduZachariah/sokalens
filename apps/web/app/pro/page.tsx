import { ResponsibleNote } from "../components/ResponsibleNote";
import { canonicalPath } from "../site";

export const metadata = {
  title: "SokaLens Pro - coming soon",
  description: "Premium SokaLens analysis is coming soon. Standard predictions and results stay free for everyone.",
  robots: { index: false },
  alternates: { canonical: canonicalPath("/pro") },
};

export default function ProPage() {
  return (
    <main className="page-shell pro-page" id="content">
      <section className="pro-hero">
        <div>
          <p className="page-kicker">SokaLens Pro</p>
          <h1>Premium analysis is coming soon.</h1>
          <p>
            SokaLens is preparing deeper football analysis. Standard predictions, fixtures, and
            results remain free for everyone in the meantime.
          </p>
        </div>
        <div className="pro-hero__signal" aria-label="Pro principles">
          <span className="eyebrow">Built for clarity</span>
          <strong>Honest analysis, when it is ready</strong>
          <p>We publish only what the data supports, and every prediction is tracked openly.</p>
        </div>
      </section>
      <ResponsibleNote />
    </main>
  );
}
