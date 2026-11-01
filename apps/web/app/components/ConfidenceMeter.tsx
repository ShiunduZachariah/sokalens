import { clampPercent, formatConfidence } from "../lib/format";

type ConfidenceMeterProps = {
  value: number;
  label?: string;
};

export function ConfidenceMeter({ value, label = "Confidence" }: ConfidenceMeterProps) {
  const safeValue = clampPercent(value);

  return (
    <div className="confidence-meter" aria-label={`${label}: ${formatConfidence(safeValue)}`}>
      <div className="confidence-meter__label">
        <span className="micro">{label}</span>
        <strong>{formatConfidence(safeValue)}</strong>
      </div>
      <div className="confidence-meter__track" aria-hidden="true">
        <div className="confidence-meter__fill" style={{ width: formatConfidence(safeValue) }} />
      </div>
    </div>
  );
}
