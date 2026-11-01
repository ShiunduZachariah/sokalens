import type { RecommendationTag } from "../lib/recommendationTags";

/**
 * A row of "why this is here" chips.
 *
 * ## The cap is the point
 *
 * `max` defaults to three. The tag functions return up to six, and a card
 * wearing six chips has told the reader nothing — they stop being signal at
 * about the fourth. The functions order them most-asserted first, so truncating
 * keeps what the engine said and drops what arithmetic said.
 *
 * ## `title`, not a tooltip component
 *
 * Each chip's `detail` names the figure it came from, and it rides on the
 * native `title` attribute. A hover card would be a better reading experience
 * and would also be invisible on the phone where most of this is read; the
 * figure itself is on the same card either way, so the chip is a label on
 * something visible rather than the only place a number appears.
 *
 * Renders nothing when there are no tags, so a fixture the engine has no
 * particular opinion about shows no empty row.
 */
export function RecommendationTags({
  className,
  max = 3,
  tags,
}: {
  className?: string;
  max?: number;
  tags: RecommendationTag[];
}) {
  if (tags.length === 0) return null;
  return (
    <ul className={["rt", className].filter(Boolean).join(" ")}>
      {tags.slice(0, max).map((tag) => (
        <li className={`rt__tag rt__tag--${tag.tone}`} key={tag.key} title={tag.detail}>
          {tag.label}
        </li>
      ))}
    </ul>
  );
}
