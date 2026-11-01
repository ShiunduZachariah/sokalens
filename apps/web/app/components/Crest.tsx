/* eslint-disable @next/next/no-img-element */
import { hasImage, imageSrc } from "../lib/imageSrc";

/**
 * A circular badge for a competition or a club.
 *
 * Falls back to initials rather than to a placeholder image, because the
 * fallback is the common case: the provider supplies no logo for a large share
 * of the catalogue, and a grid of identical grey shields is worse than a grid of
 * distinguishable letter pairs. It is also the only sound way to handle the
 * provider's sentinel values -- `imageSrc` rejects an empty string, a bare
 * filename and the literal "null", any of which would otherwise resolve against
 * the current directory and request the page itself as an image.
 *
 * A plain img tag rather than `next/image`: these are third-party provider URLs
 * on arbitrary hosts, and routing hundreds of them per page through the
 * optimiser means hundreds of origin fetches to serve a 44px circle.
 * `loading="lazy"` is required by `imageLoading.test.mjs` -- React 19 preloads
 * images in the initial shell unless they are lazy, and these decorative crests
 * once raced the stylesheet the h1 needs in order to paint.
 */
export function Crest({
  logoUrl,
  name,
  size = "md",
}: {
  logoUrl?: string;
  name: string;
  size?: "sm" | "md";
}) {
  const initials = name
    .split(/\s+/)
    .filter((word) => /[a-z0-9]/i.test(word))
    .slice(0, 2)
    .map((word) => word[0])
    .join("")
    .toUpperCase() || "—";

  return (
    <span aria-hidden="true" className={`sl-crest sl-crest--${size}`}>
      {/*
        `imageSrc(...)` is called inline rather than hoisted into a variable
        because `scripts/seo-invariants.mjs` reads the `src` attribute
        syntactically: it requires the expression to begin with `imageSrc(`, and
        a hoisted `src={src}` fails the gate even though it is the same value.
      */}
      {hasImage(logoUrl) ? <img alt="" loading="lazy" src={imageSrc(logoUrl)} /> : initials}
    </span>
  );
}
