import type { ReactNode } from "react";

import { StatusBadge } from "./StatusBadge";
import { adminApi } from "../data/adminApi";

type DataSourceKind = "internal" | "design" | "unavailable";

type DataSourceNoticeProps = {
  children: ReactNode;
  kind: DataSourceKind;
  title: string;
};

export type InternalProviderStatus = {
  provider: string;
  displayName: string;
  enabled: boolean;
  status: string;
  throttleUntil?: string;
  quota: {
    dailyLimit: number;
    dailyRemaining: number;
    dailyReserve: number;
    minuteLimit: number;
    minuteRemaining: number;
    minuteReserve: number;
    updatedAt: string;
  };
};

export type ProviderStatusSnapshot = {
  providers: InternalProviderStatus[];
  source: "admin" | "local-status" | "unavailable";
  error?: string;
};

type ProviderStatusPayload = {
  providers?: Array<{
    provider?: unknown;
    display_name?: unknown;
    enabled?: unknown;
    status?: unknown;
    throttle_until?: unknown;
    quota?: {
      daily_limit?: unknown;
      daily_remaining?: unknown;
      daily_reserve?: unknown;
      minute_limit?: unknown;
      minute_remaining?: unknown;
      minute_reserve?: unknown;
      updated_at?: unknown;
    };
  }>;
};

const noticeStatus: Record<DataSourceKind, { label: string; status: string }> = {
  internal: { label: "Internal snapshot", status: "info" },
  design: { label: "Design data", status: "warning" },
  unavailable: { label: "Data unavailable", status: "not_ready" },
};

function asNumber(value: unknown) {
  const number = Number(value);
  return Number.isFinite(number) && number >= 0 ? number : 0;
}

function asString(value: unknown) {
  return typeof value === "string" ? value : "";
}

function parseProviderStatus(payload: ProviderStatusPayload): InternalProviderStatus[] {
  if (!Array.isArray(payload.providers)) {
    return [];
  }

  return payload.providers
    .filter((provider) => typeof provider.provider === "string" && provider.provider.length > 0)
    .map((provider) => ({
      provider: asString(provider.provider),
      displayName: asString(provider.display_name) || asString(provider.provider),
      enabled: provider.enabled === true,
      status: asString(provider.status) || "unknown",
      throttleUntil: asString(provider.throttle_until) || undefined,
      quota: {
        dailyLimit: asNumber(provider.quota?.daily_limit),
        dailyRemaining: asNumber(provider.quota?.daily_remaining),
        dailyReserve: asNumber(provider.quota?.daily_reserve),
        minuteLimit: asNumber(provider.quota?.minute_limit),
        minuteRemaining: asNumber(provider.quota?.minute_remaining),
        minuteReserve: asNumber(provider.quota?.minute_reserve),
        updatedAt: asString(provider.quota?.updated_at),
      },
    }));
}

export async function fetchProviderStatusSnapshot(): Promise<ProviderStatusSnapshot> {
  try {
    const payload = await adminApi<ProviderStatusPayload>("/v1/admin/providers/status");
    const providers = parseProviderStatus(payload ?? {});
    if (providers.length === 0) {
      return {
        providers: [],
        source: "unavailable",
        error: "Internal status API returned no provider records.",
      };
    }

    return { providers, source: "admin" };
  } catch {
    return {
      providers: [],
      source: "unavailable",
      error: "Internal status API could not be reached.",
    };
  }
}

export function DataSourceNotice({ children, kind, title }: DataSourceNoticeProps) {
  const badge = noticeStatus[kind];

  return (
    <aside className="control-strip" aria-label={title} aria-live={kind === "unavailable" ? "polite" : undefined}>
      <p>
        <strong>{title}.</strong> {children}
      </p>
      <StatusBadge status={badge.status}>{badge.label}</StatusBadge>
    </aside>
  );
}
