import type { Metadata } from "next";

import {
  DataSourceNotice,
  fetchProviderStatusSnapshot,
  type InternalProviderStatus,
} from "../components/DataSourceNotice";
import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

type ProviderRequestRow = {
  id: number;
  provider: string;
  endpoint: string;
  status_code: number;
  latency_ms: number;
  quota_cost: number;
  requested_at: string;
  error_category: string;
};

type ProviderStatusRow = {
  provider: string;
  configured: string;
  ingestionState: string;
  stateTone: string;
  dailyQuota: string;
  minuteQuota: string;
  snapshotUpdated: string;
};

export const metadata: Metadata = {
  title: "Provider Operations | SokaLens Admin",
  description: "SokaLens internal provider ingestion and last-known quota status.",
};

const providerStatusColumns: DataTableColumn<ProviderStatusRow>[] = [
  { key: "provider", header: "Provider" },
  { key: "configured", header: "Configured" },
  {
    key: "ingestionState",
    header: "Worker ingestion state",
    render: (row) => <StatusBadge status={row.stateTone}>{row.ingestionState}</StatusBadge>,
  },
  { key: "dailyQuota", header: "Daily remaining / limit", align: "right" },
  { key: "minuteQuota", header: "Minute remaining / limit", align: "right" },
  { key: "snapshotUpdated", header: "Quota snapshot", priority: "secondary" },
];

const requestColumns: DataTableColumn<ProviderRequestRow>[] = [
  { key: "id", header: "ID" },
  { key: "provider", header: "Provider" },
  { key: "endpoint", header: "Endpoint" },
  {
    key: "status_code",
    header: "HTTP",
    render: (row) => (
      <StatusBadge status={row.status_code >= 400 ? "error" : "ok"}>{String(row.status_code)}</StatusBadge>
    ),
  },
  { key: "latency_ms", header: "Latency", align: "right" },
  { key: "quota_cost", header: "Cost", align: "right" },
  { key: "requested_at", header: "Requested", priority: "secondary" },
  { key: "error_category", header: "Error", priority: "secondary" },
];

function quotaLabel(remaining: number, limit: number, reserve: number) {
  return limit > 0 ? `${remaining} / ${limit} (reserve ${reserve})` : "Not reported";
}

function ingestionStateLabel(provider: InternalProviderStatus) {
  switch (provider.status.toLowerCase()) {
    case "ready":
      return "Enabled, not throttled";
    case "throttled":
      return provider.throttleUntil ? `Throttled until ${provider.throttleUntil}` : "Throttled";
    case "disabled":
      return "Disabled";
    case "degraded":
      return "Degraded ingestion state";
    case "unavailable":
      return "Ingestion state unavailable";
    default:
      return "Unknown ingestion state";
  }
}

function snapshotLabel(provider: InternalProviderStatus) {
  if (provider.quota.dailyLimit <= 0 || !provider.quota.updatedAt) return "No quota snapshot";
  const updatedAt = new Date(provider.quota.updatedAt);
  return Number.isNaN(updatedAt.valueOf()) ? "Invalid timestamp" : updatedAt.toISOString();
}

function toProviderStatusRow(provider: InternalProviderStatus): ProviderStatusRow {
  return {
    provider: provider.displayName,
    configured: provider.enabled ? "Yes" : "No",
    ingestionState: ingestionStateLabel(provider),
    stateTone: provider.status,
    dailyQuota: quotaLabel(provider.quota.dailyRemaining, provider.quota.dailyLimit, provider.quota.dailyReserve),
    minuteQuota: quotaLabel(provider.quota.minuteRemaining, provider.quota.minuteLimit, provider.quota.minuteReserve),
    snapshotUpdated: snapshotLabel(provider),
  };
}

export default async function ProviderPage() {
  const providerSnapshot = await fetchProviderStatusSnapshot();
  const providerRows = providerSnapshot.providers.map(toProviderStatusRow);
  const requestsResponse = await adminApi<{ requests: ProviderRequestRow[] }>(
    "/v1/admin/provider-requests?limit=100",
  );
  const requests = requestsResponse?.requests ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Ingestion</p>
          <h1>SokaLens Provider Operations</h1>
          <p>Review last-known worker ingestion and quota state without calling an upstream provider.</p>
        </div>
      </header>

      <DataSourceNotice kind={providerRows.length > 0 ? "internal" : "unavailable"} title="Provider status source">
        {providerRows.length > 0
          ? `Read from the authenticated admin API. This is stored quota and ingestion state, not an upstream reachability test.`
          : `${providerSnapshot.error ?? "No internal provider status is available."} No sample values are substituted.`}
      </DataSourceNotice>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Internal API</p>
            <h2>Last-known Provider State</h2>
          </div>
          <span className="panel__meta">Read-only; no provider request is made by this page</span>
        </div>
        <DataTable
          caption="Last-known internal provider ingestion and quota state"
          columns={providerStatusColumns}
          emptyLabel="Internal provider status is currently unavailable."
          getRowKey={(row) => row.provider}
          rows={providerRows}
        />
      </section>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Worker log</p>
            <h2>Recent Provider Requests</h2>
          </div>
          <span className="panel__meta">{requests.length} shown, newest first</span>
        </div>
        <DataTable
          caption="Recent provider requests"
          columns={requestColumns}
          emptyLabel="No provider requests have been logged yet."
          getRowKey={(row) => String(row.id)}
          rows={requests}
        />
      </section>
    </div>
  );
}
