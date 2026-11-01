import type { ReactNode } from "react";

type StatusBadgeProps = {
  children: ReactNode;
  status?: string;
};

const toneByStatus: Record<string, string> = {
  active: "healthy",
  allowed: "healthy",
  delivered: "healthy",
  draft: "neutral",
  failed: "failed",
  failed_payment: "failed",
  healthy: "healthy",
  locked: "info",
  manual_override: "warning",
  not_ready: "neutral",
  past_due: "failed",
  paused: "paused",
  pending: "queued",
  postponed: "warning",
  published: "healthy",
  queued: "queued",
  ready: "info",
  degraded: "warning",
  disabled: "paused",
  restricted: "failed",
  review: "warning",
  running: "running",
  stale: "stale",
  throttled: "warning",
  trialing: "info",
  unavailable: "failed",
  unknown: "neutral",
  upcoming: "info",
  warning: "warning"
};

export function StatusBadge({ children, status }: StatusBadgeProps) {
  const key = String(status ?? children)
    .toLowerCase()
    .trim()
    .replaceAll(" ", "_");
  const tone = toneByStatus[key] ?? "neutral";

  return <span className={`status-badge status-badge--${tone}`}>{children}</span>;
}
