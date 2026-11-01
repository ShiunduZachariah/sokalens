/**
 * One icon from the SokaLens match-event set.
 *
 * ## Why a sprite, when the old timeline icons were inline
 *
 * `MatchEventIcon` drew nine glyphs inline and said so in a comment: a font or a
 * sprite would be "a network request the timeline blocks on for something that is
 * decoration". That was right for nine. It is wrong for thirty-nine.
 *
 * Inlining the set would put every path the page uses into the HTML of every page
 * that uses one, repeated per occurrence — a timeline with twelve events carries
 * twelve copies. The sprite is one request, cached across every page for the life
 * of the deploy, and `<use>` renders from the cache after that. The first paint
 * blocks on nothing: a missing sprite leaves the row's label, which is what
 * carries the meaning anyway.
 *
 * ## Two consequences of referencing an external sprite
 *
 * **It must be same-origin.** `<use href="/icons/...">` resolves against this
 * origin, so it works; a CDN on another host would need CORS, and silently render
 * nothing without it.
 *
 * **Page CSS cannot reach inside the referenced symbol.** `fill="currentColor"`
 * still inherits from this element, so colour works — but the `.back` and
 * `.front` classes on `second-yellow-red` cannot be targeted from `globals.css`
 * across that boundary. That icon therefore renders in ONE colour, which its own
 * documentation says it is drawn to survive. If the two-tone version is wanted,
 * that one icon has to be inlined.
 *
 * Every icon is `aria-hidden` by default. The meaning is carried by the label
 * beside it ("Goal (Penalty)", "Second yellow"), and announcing a picture of a
 * ball before the word "Goal" makes a screen reader say the same thing twice.
 * Pass a `title` only where the icon is genuinely the only content.
 */
export type SokaIconName =
  | "yellow-card" | "red-card" | "second-yellow-red" | "corner-kick" | "penalty"
  | "free-kick" | "injury" | "substitution" | "offside" | "var" | "goal"
  | "whistle" | "own-goal" | "assist" | "penalty-scored" | "penalty-missed"
  | "throw-in" | "foul" | "var-check" | "var-overturned" | "shot"
  | "shot-on-target" | "shot-off-target" | "shot-blocked" | "shot-on-post"
  | "goalkeeper-save" | "penalty-save" | "kickoff" | "half-time" | "second-half"
  | "full-time" | "extra-time" | "added-time" | "match-delayed"
  | "match-postponed" | "match-cancelled" | "abandoned" | "lineups" | "referee";

/**
 * Served from `public/`, so the path is absolute and not locale-prefixed — a
 * `/sw/icons/...` would 404 on every localised route.
 */
const SPRITE = "/icons/sokalens-icons-sprite.svg";

export function SokaIcon({
  className,
  name,
  size = 16,
  title,
}: {
  className?: string;
  name: SokaIconName;
  size?: number;
  /** Only when the icon is the sole content; otherwise the label carries it. */
  title?: string;
}) {
  return (
    <svg
      aria-hidden={title ? undefined : true}
      className={["sl-icon", `sl-icon-${name}`, className].filter(Boolean).join(" ")}
      focusable="false"
      height={size}
      role={title ? "img" : undefined}
      width={size}
    >
      {title ? <title>{title}</title> : null}
      <use href={`${SPRITE}#sl-${name}`} />
    </svg>
  );
}
