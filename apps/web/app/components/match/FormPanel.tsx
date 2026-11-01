import Link from "next/link";
import { teamPath } from "../../lib/catalogSlug";
import { localePath } from "../../i18n/config";
import type { Locale } from "../../i18n/config";

export type FormSide = {
  /** Raw form string from the fixture feed, e.g. "L W D D W". */
  form: string;
  name: string;
  slug: string;
};

type Props = { away: FormSide; home: FormSide; locale: Locale };

const RESULT_LABEL: Record<string, string> = { W: "Win", D: "Draw", L: "Loss" };

function results(form: string) {
  return form
    .trim()
    .toUpperCase()
    .split(/\s+/)
    .filter((item) => item === "W" || item === "D" || item === "L")
    .slice(0, 5);
}

/** Three points a win, one a draw — the same arithmetic the artboard states. */
function points(items: string[]) {
  return items.reduce((total, item) => total + (item === "W" ? 3 : item === "D" ? 1 : 0), 0);
}

/** "2W 1D 2L", in that fixed order regardless of the sequence played. */
function record(items: string[]) {
  return (["W", "D", "L"] as const)
    .map((key) => `${items.filter((item) => item === key).length}${key}`)
    .join(" ");
}

/**
 * Says which side arrives in better form, or that they are level.
 *
 * Within one point is called close rather than named as an edge: with a
 * five-match sample a single point is one drawn game, and claiming an
 * advantage from it would overstate what the numbers support.
 */
function formNote(home: FormSide, away: FormSide, homePoints: number, awayPoints: number) {
  if (Math.abs(homePoints - awayPoints) <= 1) {
    return `Recent form is close: ${homePoints} points for ${home.name} against ${awayPoints} for ${away.name}.`;
  }
  const stronger = homePoints > awayPoints ? home : away;
  return `${stronger.name} arrive in better form: ${Math.max(homePoints, awayPoints)} points from 5 against ${Math.min(homePoints, awayPoints)}.`;
}

function TeamForm({ away, locale, side }: { away: boolean; locale: Locale; side: FormSide }) {
  const items = results(side.form);
  const href = teamPath(side.slug);
  return (
    <div className="mp-form-team">
      <div className="mp-form-team__head">
        <span className="mp-form-team__name">
          <i aria-hidden="true" className={away ? "mp-dot mp-dot--away" : "mp-dot"} />
          {href ? <Link href={localePath(href, locale)}>{side.name}</Link> : side.name}
        </span>
        {items.length > 0 ? <span className="mp-form-team__record">{record(items)}</span> : null}
      </div>
      {items.length > 0 ? (
        <div className="mp-form-row">
          {items.map((item, index) => (
            <span
              aria-label={RESULT_LABEL[item]}
              className={`mp-chip mp-chip--${item}`}
              key={`${item}-${index}`}
              title={RESULT_LABEL[item]}
            >
              {item}
            </span>
          ))}
          <span className="mp-form-team__points">
            <strong>{points(items)}</strong>
            <span>PTS</span>
          </span>
        </div>
      ) : (
        <p className="mp-panel__note" style={{ borderTop: 0, paddingTop: 0 }}>
          No recent form recorded for this side yet.
        </p>
      )}
    </div>
  );
}

export function FormPanel({ away, home, locale }: Props) {
  const homeItems = results(home.form);
  const awayItems = results(away.form);
  const bothKnown = homeItems.length > 0 && awayItems.length > 0;

  return (
    <div className="mp-panel">
      <div className="mp-panel__head">
        <h3>Last 5 matches</h3>
        <span className="mp-panel__legend">
          <span>W win</span>
          <span>D draw</span>
          <span>L loss</span>
        </span>
      </div>
      <TeamForm away={false} locale={locale} side={home} />
      <TeamForm away locale={locale} side={away} />
      {/* Only compared when both sides have a run to compare. */}
      {bothKnown ? (
        <p className="mp-panel__note">
          {formNote(home, away, points(homeItems), points(awayItems))}
        </p>
      ) : null}
    </div>
  );
}
