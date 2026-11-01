type IconProps = {
  className?: string;
  size?: number;
};

type IconPath = { d: string; filled?: boolean };

const iconPaths = {
  account: [{ d: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM4.5 20a7.5 7.5 0 0 1 15 0" }],
  builder: [{ d: "M5 7h14M5 12h14M5 17h14M8 5v4M16 10v4M11 15v4" }],
  calendar: [{ d: "M7 3v3M17 3v3M4 9h16M6 5h12a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H6a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2Z" }],
  chart: [{ d: "M5 19V9M12 19V5M19 19v-7" }],
  heart: [{ d: "M20.8 4.7a5.5 5.5 0 0 0-7.8 0L12 5.8l-1-1.1a5.5 5.5 0 1 0-7.8 7.8L12 21l8.8-8.5a5.5 5.5 0 0 0 0-7.8Z" }],
  home: [{ d: "M3 9.5 12 3l9 6.5V20a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z" }, { d: "M9 22v-8h6v8" }],
  inplay: [
    { d: "M12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z", filled: true },
    { d: "M6.3 6.3a8 8 0 0 0 0 11.4M17.7 6.3a8 8 0 0 1 0 11.4" },
  ],
  results: [{ d: "M9 12l2 2 4-5" }, { d: "M12 21a9 9 0 1 1 0-18 9 9 0 0 1 0 18Z" }],
  search: [{ d: "m21 21-4.3-4.3M10.8 18a7.2 7.2 0 1 1 0-14.4 7.2 7.2 0 0 1 0 14.4Z" }],
  shield: [{ d: "M12 3 19 6v6c0 4.2-2.5 7.4-7 9-4.5-1.6-7-4.8-7-9V6l7-3Z" }],
  trophy: [{ d: "M8 21h8M12 17v4M7 4h10v5a5 5 0 0 1-10 0V4ZM5 6H3v2a4 4 0 0 0 4 4M19 6h2v2a4 4 0 0 1-4 4" }],
} satisfies Record<string, IconPath[]>;

export type IconName = keyof typeof iconPaths;

export function Icon({ className, name, size = 20 }: IconProps & { name: IconName }) {
  return (
    <svg
      aria-hidden="true"
      className={className}
      fill="none"
      height={size}
      viewBox="0 0 24 24"
      width={size}
    >
      {/*
        Every icon in this set is stroked, with one exception: the in-play live
        dot is filled. A hollow ring reads as "paused", which is the opposite of
        what the icon is signalling, so a filled member is supported rather than
        approximating the dot with a thick stroke.
      */}
      {(iconPaths[name] as IconPath[]).map((path) => (
        <path
          d={path.d}
          fill={path.filled ? "currentColor" : "none"}
          key={path.d}
          stroke={path.filled ? "none" : "currentColor"}
          strokeLinecap="round"
          strokeLinejoin="round"
          strokeWidth="2"
        />
      ))}
    </svg>
  );
}
