import type { CSSProperties } from "react";

import { crestUrl } from "../lib/crestProxy";
import { hasImage, imageSrc } from "../lib/imageSrc";

/**
 * The one way a team is shown on this site: its crest, at a fixed size, with an
 * initials shield when there is no usable logo.
 *
 * Why one primitive. Three overlapped before the 2026-10-15 review -- this one
 * (initials only, no logo path at all), `Crest` (logo + initials, but a circle
 * at 34/44px) and `TeamIdentity`'s own inline 28px image. The review
 * asks for a crest beside every team name on every surface, which is the point
 * at which three near-identical components stop being survivable: a size the
 * spec names has to exist in one place, and the fallback has to behave the same
 * everywhere or a missing logo looks like a bug on one page and a design on
 * another. `Crest` stays, but only for competitions -- a league badge is a
 * circle, a club is a shield, and that is a real visual distinction rather than
 * two spellings of the same thing.
 *
 * Sizes are the review's, not arbitrary: 16 dense lists and ACCA legs on
 * mobile, 20 prediction rows, 24 cards, 32+ match headers and the team page.
 * Width and height are always set, so nothing shifts while the image loads --
 * that is the whole reason the box is fixed rather than sized by its content.
 *
 * Accessibility: the image is `alt=""` and `aria-hidden`, and the shield is too.
 * The team name next to it is the accessible label. A crest that announced the
 * team would make every row say the name twice.
 */

type CrestSize = 16 | 20 | 24 | 32 | 44;

type TeamCrestProps = {
  /** Provider logo URL. Anything unusable falls through to the shield. */
  logoUrl?: string;
  name: string;
  size?: CrestSize;
};

/**
 * Club suffixes carry no identity, so they must not win a letter. "Connah's
 * Quay Nomads FC" is CN, not CF, and "Arsenal U21" is AR rather than AU.
 */
const NOISE = new Set([
  "fc",
  "sc",
  "ac",
  "afc",
  "cf",
  "sk",
  "fk",
  "cd",
  "ud",
  "if",
  "bk",
  "u19",
  "u20",
  "u21",
  "u23",
  "ii",
  "b",
  "reserves",
  "women",
  "w",
]);

export function teamInitials(name: string): string {
  const words = name
    .split(/[\s.·-]+/)
    .map((word) => word.replace(/[^\p{L}\p{N}]/gu, ""))
    .filter((word) => word.length > 0 && !NOISE.has(word.toLowerCase()));

  // Every word was noise ("FC"), or the name was punctuation: fall back to the
  // raw string rather than rendering an empty shield.
  if (words.length === 0) {
    const bare = name.replace(/[^\p{L}\p{N}]/gu, "");
    return (bare.slice(0, 2) || "—").toUpperCase();
  }
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[words.length - 1][0]).toUpperCase();
}

/**
 * Review 2.1: `hsl(hash(name) % 360, 52%, 40%)` with white text. Fixed
 * saturation and lightness are what make the whole set legible -- a per-team
 * random colour would eventually pick one that fails against white, and the
 * shield's text is always white. At L=40% every hue clears 4.5:1 on white.
 */
export function crestColor(name: string): string {
  let hash = 0;
  for (let index = 0; index < name.length; index += 1) {
    hash = (hash * 31 + name.charCodeAt(index)) | 0;
  }
  return `hsl(${Math.abs(hash) % 360}, 52%, 40%)`;
}

export function TeamCrest({ logoUrl, name, size = 24 }: TeamCrestProps) {
  if (hasImage(logoUrl)) {
    return (
      /*
        A plain img, not next/image: these are third-party provider URLs on
        arbitrary hosts, and routing hundreds of 20px crests per page through
        the optimiser means hundreds of origin fetches.

        `loading="lazy"` is required by components/imageLoading.test.mjs --
        React 19 preloads images in the initial shell unless they are lazy, and
        these decorative crests once raced the stylesheet the h1 needs to paint.

        `src={imageSrc(...)}` is called inline and must stay inline:
        scripts/seo-invariants.mjs reads the attribute syntactically and
        requires the expression to begin with `imageSrc(`. A hoisted
        `src={src}` fails the gate on an identical value.

        crestUrl() composes INSIDE that guard rather than replacing it. It
        returns our own /api/crest path for a provider host we proxy, and the
        provider URL unchanged for one we do not -- both of which imageSrc
        accepts, and neither of which can smuggle an unguarded value into the
        attribute. Wrapping the other way round would have failed the gate for
        no gain.
      */
      // eslint-disable-next-line @next/next/no-img-element
      <img
        alt=""
        aria-hidden="true"
        className={`sl-teamcrest sl-teamcrest--${size} sl-teamcrest--logo`}
        decoding="async"
        height={size}
        loading="lazy"
        src={imageSrc(crestUrl(logoUrl))}
        width={size}
      />
    );
  }

  return (
    <span
      aria-hidden="true"
      className={`sl-teamcrest sl-teamcrest--${size} sl-teamcrest--shield`}
      style={{ "--sl-crest-fill": crestColor(name) } as CSSProperties}
    >
      {teamInitials(name)}
    </span>
  );
}
