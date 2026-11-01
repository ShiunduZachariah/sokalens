/**
 * The refresh's inline icons.
 *
 * Inline rather than an icon font or sprite: each is a handful of stroke paths,
 * they inherit `currentColor` so they follow the theme tokens without a second
 * set of rules, and the design uses only these six.
 */
type IconProps = { size?: number };

const base = {
  "aria-hidden": true,
  fill: "none",
  stroke: "currentColor",
  viewBox: "0 0 24 24",
} as const;

export function PlusIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2.4} width={size}>
      <path d="M12 5v14M5 12h14" />
    </svg>
  );
}

export function ShareIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2} width={size}>
      <circle cx="18" cy="5" r="2.5" />
      <circle cx="6" cy="12" r="2.5" />
      <circle cx="18" cy="19" r="2.5" />
      <path d="M8.2 10.9l7.6-4.6M8.2 13.1l7.6 4.6" />
    </svg>
  );
}

export function ClockIcon({ size = 12 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2} width={size}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 7v5l3 2" />
    </svg>
  );
}

export function ArrowRightIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2} width={size}>
      <path d="M5 12h14M13 6l6 6-6 6" />
    </svg>
  );
}

export function CheckIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2.4} width={size}>
      <path d="M4 12.5l5 5L20 6.5" />
    </svg>
  );
}

/**
 * The hero's pitch line-art: halfway line, centre circle and both penalty
 * areas. Purely decorative, drawn in --sl-deco, and hidden below 900px where
 * the hero has no room for it.
 */
export function PitchLines() {
  return (
    <svg
      aria-hidden="true"
      className="mp-hero__deco"
      fill="none"
      height="420"
      stroke="currentColor"
      strokeWidth="2"
      viewBox="0 0 1200 420"
      width="1200"
    >
      <path d="M600 0v420" />
      <circle cx="600" cy="170" r="120" />
      <circle cx="600" cy="170" r="5" fill="currentColor" />
      <rect height="260" width="170" x="-2" y="40" />
      <rect height="260" width="170" x="1032" y="40" />
    </svg>
  );
}

export function TickIcon({ size = 14 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2.4} width={size}>
      <path d="M5 12l5 5 9-10" />
    </svg>
  );
}

export function ArrowUpRightIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2.2} width={size}>
      <path d="M7 17L17 7M9 7h8v8" />
    </svg>
  );
}

/**
 * The home hero's concentric-circle motif. Decorative; hidden below 1180px
 * where the hero drops to a single column and has no room beside the copy.
 */
export function TargetRings() {
  return (
    <svg
      aria-hidden="true"
      className="hm-hero__deco"
      fill="none"
      height="560"
      stroke="currentColor"
      strokeWidth="2"
      viewBox="0 0 760 560"
      width="760"
    >
      <circle cx="440" cy="300" r="250" />
      <circle cx="440" cy="300" r="150" />
      <circle cx="440" cy="300" r="50" />
      <path d="M440 0v560M100 300h680" />
    </svg>
  );
}

/**
 * The ACCA hero's ring motif. Decorative; hidden below 1180px where the hero
 * collapses to one column.
 */
export function AccaRings() {
  return (
    <svg
      aria-hidden="true"
      className="ac-hero__deco"
      fill="none"
      height="420"
      stroke="currentColor"
      strokeWidth="2"
      viewBox="0 0 520 420"
      width="520"
    >
      <circle cx="420" cy="120" r="160" />
      <circle cx="420" cy="120" r="90" />
      <path d="M0 300h520M260 0v420" />
    </svg>
  );
}

export function ChevronRightIcon({ size = 16 }: IconProps) {
  return (
    <svg {...base} height={size} strokeWidth={2} width={size}>
      <path d="M9 6l6 6-6 6" />
    </svg>
  );
}
