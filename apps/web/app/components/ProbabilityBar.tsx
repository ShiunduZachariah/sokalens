type ProbabilityTone = "home" | "draw" | "away" | "pitch" | "signal" | "muted";

type ProbabilityBarProps = {
  className?: string;
  detail?: string;
  label: string;
  tone?: ProbabilityTone;
  value: number;
};

export function ProbabilityBar({
  className,
  detail,
  label,
  tone = "signal",
  value,
}: ProbabilityBarProps) {
  const safeValue = Math.max(0, Math.min(100, value));
  const roundedValue = Math.round(safeValue);
  const classes = ["probability-bar", `probability-bar--${tone}`, className]
    .filter(Boolean)
    .join(" ");

  return (
    <div className={classes}>
      <div className="probability-bar__header">
        <span className="probability-bar__label">{label}</span>
        <strong className="probability-bar__value">{roundedValue}%</strong>
      </div>
      <div
        aria-label={`${label}: ${roundedValue}%`}
        aria-valuemax={100}
        aria-valuemin={0}
        aria-valuenow={roundedValue}
        className="probability-bar__track"
        role="progressbar"
      >
        <span className="probability-bar__fill" style={{ width: `${safeValue}%` }} />
      </div>
      {detail ? <span className="probability-bar__detail">{detail}</span> : null}
    </div>
  );
}
