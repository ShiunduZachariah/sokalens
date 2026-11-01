import type { PreviewSection } from "../lib/matchPreview";

/**
 * The written preview. Rendered outside the tabs so it is visible on first
 * paint whichever tab is selected, and as plain paragraphs because that is
 * what it is: text a reader reads, and the fixture-specific copy a crawler
 * needs to tell 7,869 fixture pages apart.
 */
export function MatchPreview({ homeTeam, awayTeam, sections }: { homeTeam: string; awayTeam: string; sections: PreviewSection[] }) {
  return (
    <section className="section match-preview" id="preview" aria-labelledby="match-preview-title">
      <div className="section-header">
        <div>
          <h2 id="match-preview-title">{homeTeam} vs {awayTeam} preview</h2>
          <p>Written from each side&apos;s recorded results and the published prediction.</p>
        </div>
      </div>
      {sections.map((section) => (
        <div className="match-preview__block" key={section.heading}>
          <h3>{section.heading}</h3>
          {section.style === "list" ? (
            <ul className="match-preview__list">
              {section.sentences.map((sentence) => <li key={sentence}>{sentence}</li>)}
            </ul>
          ) : (
            <p>{section.sentences.join(" ")}</p>
          )}
        </div>
      ))}
    </section>
  );
}
