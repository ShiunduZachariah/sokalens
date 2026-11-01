import { StatusBadge } from "./StatusBadge";

type MetricCardProps = {
  label: string;
  value: string;
  detail: string;
  status: string;
};

export function MetricCard({ label, value, detail, status }: MetricCardProps) {
  return (
    <article className="metric-card">
      <div className="metric-card__header">
        <span>{label}</span>
        <StatusBadge status={status}>{status}</StatusBadge>
      </div>
      <strong>{value}</strong>
      <p>{detail}</p>
    </article>
  );
}
