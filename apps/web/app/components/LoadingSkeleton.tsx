type LoadingSkeletonProps = {
  className?: string;
  count?: number;
  label?: string;
  variant?: "card" | "row" | "chart";
};

export function LoadingSkeleton({
  className,
  count = 1,
  label = "Loading content",
  variant = "card",
}: LoadingSkeletonProps) {
  const safeCount = Math.max(1, Math.min(12, Math.floor(count)));
  const classes = ["loading-skeleton", `loading-skeleton--${variant}`, className]
    .filter(Boolean)
    .join(" ");

  return (
    <div aria-busy="true" aria-live="polite" className={classes} role="status">
      <span className="loading-skeleton__sr-only">{label}</span>
      {Array.from({ length: safeCount }, (_, index) => (
        <div aria-hidden="true" className="loading-skeleton__item" key={index}>
          <span className="loading-skeleton__line loading-skeleton__line--short" />
          <span className="loading-skeleton__line loading-skeleton__line--long" />
          <span className="loading-skeleton__line loading-skeleton__line--medium" />
        </div>
      ))}
    </div>
  );
}
