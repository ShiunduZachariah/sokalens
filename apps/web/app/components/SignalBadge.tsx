import { clampPercent, formatConfidence, riskTier, riskTierLabel } from "../lib/format";

type SignalTone =
  | "strong"
  | "medium"
  | "low"
  | "neutral"
  | "positive"
  | "warning"
  | "negative";

export type ConfidenceBand = "strong" | "medium" | "low" | "stats-only";
export type RiskLevel = "Lower" | "Balanced" | "Higher" | "lower" | "balanced" | "higher";
export type AvailabilityStatus = "full" | "partial" | "stats-only" | "unavailable";
export type AvailabilityState =
  | "available"
  | "limited"
  | "not-enough-data"
  | "unavailable"
  | "skipped"
  | "pending"
  | "stats-only";

export type SignalDefinition = {
  signal: string;
  label: string;
  tone: SignalTone;
};

type SignalBadgeProps = SignalDefinition & {
  className?: string;
  detail?: string;
  kind?: "confidence" | "risk" | "availability" | "status";
};

export function confidenceSignal(value: number): SignalDefinition & { band: ConfidenceBand } {
  const safeValue = clampPercent(value);

  if (safeValue >= 75) {
    return { band: "strong", signal: "strong", label: `Strong - ${formatConfidence(safeValue)}`, tone: "strong" };
  }
  if (safeValue >= 65) {
    return { band: "medium", signal: "medium", label: `Medium - ${formatConfidence(safeValue)}`, tone: "medium" };
  }
  if (safeValue >= 55) {
    return { band: "low", signal: "low", label: `Low - ${formatConfidence(safeValue)}`, tone: "low" };
  }
  return {
    band: "stats-only",
    signal: "stats-only",
    label: `Stats only - ${formatConfidence(safeValue)}`,
    tone: "neutral",
  };
}

export function riskSignal(level: RiskLevel): SignalDefinition {
  const normalizedLevel = level.toLowerCase() as "lower" | "balanced" | "higher";
  const toneByLevel: Record<"lower" | "balanced" | "higher", SignalTone> = {
    lower: "positive",
    balanced: "medium",
    higher: "warning",
  };
  // The tier vocabulary (Higher/Balanced/Lower) and its labels come from the
  // single shared risk scheme in lib/format so every surface tiers risk alike.
  const tier = normalizedLevel === "lower" ? "lower" : normalizedLevel === "higher" ? "higher" : "balanced";
  return {
    signal: tier,
    label: riskTierLabel(tier),
    tone: toneByLevel[tier],
  };
}

/** Risk tier derived from a confidence percentage using the shared scheme. */
export function confidenceRiskSignal(value: number): SignalDefinition {
  const tier = riskTier(value);
  const toneByTier: Record<typeof tier, SignalTone> = {
    higher: "warning",
    balanced: "medium",
    lower: "positive",
  };
  return { signal: tier, label: riskTierLabel(tier), tone: toneByTier[tier] };
}

export function availabilitySignal(state: AvailabilityState): SignalDefinition {
  const definitions: Record<AvailabilityState, SignalDefinition> = {
    available: { signal: "available", label: "Data available", tone: "positive" },
    limited: { signal: "limited", label: "Limited data", tone: "warning" },
    "not-enough-data": {
      signal: "not-enough-data",
      label: "Not enough data",
      tone: "warning",
    },
    unavailable: { signal: "unavailable", label: "Data unavailable", tone: "neutral" },
    skipped: { signal: "skipped", label: "Prediction skipped", tone: "neutral" },
    pending: { signal: "pending", label: "Analysis pending", tone: "medium" },
    "stats-only": { signal: "stats-only", label: "Stats only", tone: "neutral" },
  };

  return definitions[state];
}

export function SignalBadge({
  className,
  detail,
  kind = "status",
  label,
  signal,
  tone,
}: SignalBadgeProps) {
  const classes = [
    "signal-badge",
    `signal-badge--${kind}`,
    `signal-badge--${tone}`,
    `signal-badge--${signal}`,
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <span className={classes} data-signal={signal} title={detail}>
      <span className="signal-badge__label">{label}</span>
      {detail ? <span className="signal-badge__detail">{detail}</span> : null}
    </span>
  );
}

export function ConfidenceBadge({ value }: { value: number }) {
  return <SignalBadge {...confidenceSignal(value)} kind="confidence" />;
}

export function RiskBadge({ level }: { level: RiskLevel }) {
  return <SignalBadge {...riskSignal(level)} kind="risk" />;
}

export function AvailabilityBadge({ status }: { status: AvailabilityStatus }) {
  const stateByStatus: Record<AvailabilityStatus, AvailabilityState> = {
    full: "available",
    partial: "limited",
    "stats-only": "stats-only",
    unavailable: "unavailable",
  };

  return <SignalBadge {...availabilitySignal(stateByStatus[status])} kind="availability" />;
}
