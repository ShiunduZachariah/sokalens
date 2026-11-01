import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { MetricCard } from "../components/MetricCard";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

type RouteMetric = {
  method: string;
  route: string;
  requests: number;
  server_errors: number;
  server_error_rate: number;
  average_ms: number;
  approx_p95_ms: number;
  requests_under_3_seconds: number;
};

type Diagnostics = {
  started_at: string;
  uptime_seconds: number;
  requests: number;
  server_errors: number;
  server_error_rate: number;
  goroutines: number;
  heap_alloc_bytes: number;
  routes: RouteMetric[];
};

type CatalogueCoverage = {
  countries_total: number;
  countries_with_flag: number;
  teams_total: number;
  teams_with_logo: number;
  leagues_total: number;
  leagues_with_logo: number;
  search_indexed: number;
  live_last_seen?: string;
  worker_last_seen?: string;
};

export const metadata: Metadata = {
  title: "Diagnostics | SokaLens Admin",
  description: "Restricted runtime latency, error-rate, and process health diagnostics.",
};

const columns: DataTableColumn<RouteMetric>[] = [
  { key: "route", header: "Route", render: (row) => <code>{row.route}</code> },
  { key: "method", header: "Method" },
  { key: "requests", header: "Requests", render: (row) => String(row.requests) },
  { key: "average_ms", header: "Average", render: (row) => `${row.average_ms.toFixed(1)} ms` },
  { key: "approx_p95_ms", header: "p95 bucket", render: (row) => row.approx_p95_ms < 0 ? ">10 s" : `${row.approx_p95_ms.toFixed(0)} ms` },
  { key: "server_error_rate", header: "5xx rate", render: (row) => `${(row.server_error_rate * 100).toFixed(2)}%` },
  { key: "server_errors", header: "SLO", render: (row) => {
    const breached = row.approx_p95_ms > 3000 || row.approx_p95_ms < 0 || row.server_error_rate >= 0.005;
    return <StatusBadge status={breached ? "degraded" : "ready"}>{breached ? "Review" : "Within target"}</StatusBadge>;
  } },
];

function mediaCoverage(total: number, covered: number) {
  if (total <= 0) {
    return "—";
  }
  return `${Math.round((covered / total) * 100)}%`;
}

function formatSeen(value: string | undefined) {
  if (!value) {
    return "Not seen yet";
  }
  return new Date(value).toLocaleString();
}

export default async function DiagnosticsPage() {
  const diagnostics = await adminApi<Diagnostics>("/v1/admin/observability/diagnostics");
  const coverage = await adminApi<CatalogueCoverage>("/v1/admin/catalogue/coverage");
  const errorRate = diagnostics ? diagnostics.server_error_rate * 100 : 0;
  return (
    <div className="page-stack">
      <header className="page-header"><div><p className="eyebrow">Restricted monitoring</p><h1>Runtime diagnostics</h1><p>Bounded request latency and 5xx telemetry for the current API process. No credentials, raw URLs, or user input are exposed.</p></div></header>
      {!diagnostics ? <section className="panel"><p>Diagnostics are unavailable or this account does not have analytics permission.</p></section> : (
        <>
          <section className="metric-grid" aria-label="API runtime summary">
            <MetricCard detail="Completed requests since this process started." label="Requests" status="ready" value={String(diagnostics.requests)} />
            <MetricCard detail="Target: below 0.5%." label="5xx error rate" status={errorRate >= 0.5 ? "degraded" : "ready"} value={`${errorRate.toFixed(2)}%`} />
            <MetricCard detail={`${diagnostics.goroutines} goroutines.`} label="Runtime heap" status="ready" value={`${(diagnostics.heap_alloc_bytes / 1024 / 1024).toFixed(1)} MB`} />
            <MetricCard detail={`Started ${new Date(diagnostics.started_at).toLocaleString()}.`} label="Uptime" status="ready" value={`${Math.floor(diagnostics.uptime_seconds / 60)} min`} />
          </section>
          <section className="panel"><div className="panel__header"><div><p className="eyebrow">HTTP SLO</p><h2>Route performance</h2></div><span className="panel__meta">Targets: p95 under 3 s, 5xx under 0.5%</span></div><DataTable caption="API route performance" columns={columns} emptyLabel="No requests have been recorded yet." getRowKey={(row) => `${row.method}:${row.route}`} rows={diagnostics.routes} /></section>
        </>
      )}
      {coverage ? (
        <section className="panel" aria-labelledby="catalogue-coverage">
          <div className="panel__header">
            <div><p className="eyebrow">Reference catalogue</p><h2 id="catalogue-coverage">Catalogue & search coverage</h2></div>
            <span className="panel__meta">Live worker + provider ingestion</span>
          </div>
          <div className="metric-grid" aria-label="Catalogue coverage summary">
            <MetricCard detail={`${coverage.countries_with_flag} of ${coverage.countries_total} countries carry flags.`} label="Countries" status={coverage.countries_total > 0 && coverage.countries_with_flag === 0 ? "degraded" : "ready"} value={String(coverage.countries_total)} />
            <MetricCard detail={`${coverage.teams_with_logo} of ${coverage.teams_total} teams have logos (${mediaCoverage(coverage.teams_total, coverage.teams_with_logo)} coverage).`} label="Teams" status={coverage.teams_total > 0 && coverage.teams_with_logo === 0 ? "degraded" : "ready"} value={String(coverage.teams_total)} />
            <MetricCard detail={`${coverage.leagues_with_logo} of ${coverage.leagues_total} leagues have logos (${mediaCoverage(coverage.leagues_total, coverage.leagues_with_logo)} coverage).`} label="Leagues" status={coverage.leagues_total > 0 && coverage.leagues_with_logo === 0 ? "degraded" : "ready"} value={String(coverage.leagues_total)} />
            <MetricCard detail="Documents in the Typesense entities collection." label="Search index" status={coverage.search_indexed > 0 ? "ready" : "degraded"} value={String(coverage.search_indexed)} />
            <MetricCard detail={`Last live poll ${formatSeen(coverage.live_last_seen)}.`} label="Live pipeline" status={coverage.live_last_seen ? "ready" : "degraded"} value={coverage.live_last_seen ? "Polling" : "Idle"} />
            <MetricCard detail={`Worker last job activity ${formatSeen(coverage.worker_last_seen)}.`} label="Worker activity" status={coverage.worker_last_seen ? "ready" : "degraded"} value={coverage.worker_last_seen ? "Active" : "Idle"} />
          </div>
        </section>
      ) : (
        <section className="panel"><p>Catalogue coverage is unavailable or this account does not have analytics permission.</p></section>
      )}
    </div>
  );
}
