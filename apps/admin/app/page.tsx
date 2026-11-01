import type { Metadata } from "next";

import { DataSourceNotice, fetchProviderStatusSnapshot } from "./components/DataSourceNotice";
import { DataTable, type DataTableColumn } from "./components/DataTable";
import { MetricCard } from "./components/MetricCard";
import { StatusBadge } from "./components/StatusBadge";
import { adminApi } from "./data/adminApi";

export const metadata: Metadata = {
  title: "Operations Overview | SokaLens Admin",
  description: "SokaLens internal operations and provider ingestion overview.",
};

type JobRow = {
  id: number;
  job_key: string;
  queue: string;
  status: string;
  attempts: number;
  created_at: string;
  last_error: string;
};

type UserGrowth = {
  total_users: number;
  verified_users: number;
  unverified_users: number;
  new_today: number;
  new_7_days: number;
  new_30_days: number;
  active_7_days: number;
  active_30_days: number;
  activated_users: number;
};

const jobColumns: DataTableColumn<JobRow>[] = [
  { key: "id", header: "Job ID" },
  { key: "job_key", header: "Job" },
  { key: "queue", header: "Queue", priority: "secondary" },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge>,
  },
  { key: "attempts", header: "Attempts", align: "right" },
  { key: "last_error", header: "Last error", priority: "secondary" },
];

function ingestionSummary(status: string) {
  switch (status.toLowerCase()) {
    case "ready":
      return "Enabled, not throttled";
    case "throttled":
      return "Throttled";
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

export default async function AdminHomePage() {
  const providerSnapshot = await fetchProviderStatusSnapshot();
  const jobsResponse = await adminApi<{ jobs: JobRow[] }>("/v1/admin/jobs?limit=10");
  const growthResponse = await adminApi<{ growth: UserGrowth }>("/v1/admin/user-growth");
  const jobs = jobsResponse?.jobs ?? [];
  const failedJobs = jobs.filter((job) => job.status === "failed" || job.status === "dead_letter");
  const growth = growthResponse?.growth;

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Operations</p>
          <h1>SokaLens Operations Overview</h1>
          <p>Last-known provider ingestion state and recent worker jobs.</p>
        </div>
      </header>

      <DataSourceNotice
        kind={providerSnapshot.providers.length > 0 ? "internal" : "unavailable"}
        title="Provider status source"
      >
        {providerSnapshot.providers.length > 0
          ? `Read from the authenticated admin API. Values describe stored quota and worker ingestion state, not upstream reachability.`
          : `${providerSnapshot.error ?? "No internal provider status is available."}`}
      </DataSourceNotice>

      {growth ? (
        <section className="metric-grid" aria-label="User growth">
          <MetricCard detail={`${growth.verified_users} verified · ${growth.unverified_users} pending verification`} label="Registered users" status="live" value={growth.total_users.toLocaleString()} />
          <MetricCard detail={`${growth.new_today} today · ${growth.new_30_days} in 30 days`} label="New users" status="live" value={growth.new_7_days.toLocaleString()} />
          <MetricCard detail={`${growth.active_30_days} signed in during the last 30 days`} label="Active users (7d)" status="live" value={growth.active_7_days.toLocaleString()} />
          <MetricCard detail="Verified users who added at least one favourite" label="Activated users" status="live" value={growth.activated_users.toLocaleString()} />
        </section>
      ) : null}

      {providerSnapshot.providers.length > 0 ? (
        <section className="metric-grid" aria-label="Last-known provider ingestion state">
          {providerSnapshot.providers.map((provider) => {
            const quotaDetail =
              provider.quota.dailyLimit > 0
                ? `${provider.quota.dailyRemaining} of ${provider.quota.dailyLimit} daily requests remain; reserve ${provider.quota.dailyReserve}.`
                : "Daily quota is not reported by the internal snapshot.";

            return (
              <MetricCard
                detail={`${quotaDetail} This is not a reachability check.`}
                key={provider.provider}
                label={`${provider.displayName} ingestion`}
                status={provider.status}
                value={ingestionSummary(provider.status)}
              />
            );
          })}
        </section>
      ) : null}

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Attention</p>
            <h2>Recent Jobs</h2>
          </div>
          <span className="panel__meta">{jobs.length} shown · {failedJobs.length} failed</span>
        </div>
        <DataTable
          caption="Recent worker jobs"
          columns={jobColumns}
          emptyLabel="No jobs are available."
          getRowKey={(row) => String(row.id)}
          rows={jobs}
        />
      </section>
    </div>
  );
}
