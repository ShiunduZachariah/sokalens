import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Ads | SokaLens Admin",
  description: "SokaLens advertising operations.",
};

type AdCampaign = {
  id: number;
  campaign_key: string;
  name: string;
  status: string;
  placement_key: string;
  created_at: string;
  starts_at?: string;
  ends_at?: string;
};

type AdProvider = {
  adapter_key: string;
  code: string;
  cooldown_seconds: number;
  enabled: boolean;
  failure_threshold: number;
  global_priority: number;
  health_status: string;
  name: string;
  request_timeout_ms: number;
  traffic_weight: number;
};

const adColumns: DataTableColumn<AdCampaign>[] = [
  { key: "name", header: "Name" },
  { key: "campaign_key", header: "Campaign key", priority: "secondary" },
  { key: "placement_key", header: "Placement" },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge>,
  },
  { key: "starts_at", header: "Starts", render: (row) => row.starts_at ?? "—" },
  { key: "ends_at", header: "Ends", render: (row) => row.ends_at ?? "—" },
  { key: "created_at", header: "Created", priority: "secondary" },
];

const providerColumns: DataTableColumn<AdProvider>[] = [
  { key: "name", header: "Provider" },
  { key: "adapter_key", header: "Adapter", priority: "secondary" },
  { key: "global_priority", header: "Priority" },
  { key: "traffic_weight", header: "Initial weight" },
  {
    key: "health_status",
    header: "Health",
    render: (row) => <StatusBadge status={row.health_status}>{row.health_status}</StatusBadge>,
  },
  { key: "enabled", header: "Enabled", render: (row) => row.enabled ? "Yes" : "No" },
  { key: "request_timeout_ms", header: "Timeout", render: (row) => `${row.request_timeout_ms} ms` },
  { key: "cooldown_seconds", header: "Cooldown", priority: "secondary", render: (row) => `${row.cooldown_seconds}s` },
];

export default async function Page() {
  const response = await adminApi<{ campaigns: AdCampaign[]; providers: AdProvider[] }>("/v1/admin/ads?limit=100");
  const campaigns = response?.campaigns ?? [];
  const providers = response?.providers ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Commercial</p>
          <h1>Ads</h1>
          <p>SokaLens advertising operations.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <h2>Fallback hierarchy</h2>
            <p>Initial traffic weight, current health, and failover order.</p>
          </div>
          <span className="panel__meta">{providers.length} configured</span>
        </div>
        <DataTable
          caption="Advertising provider fallback hierarchy"
          columns={providerColumns}
          emptyLabel="No advertising providers are configured."
          getRowKey={(row) => row.code}
          rows={providers}
        />
      </section>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Campaigns</p>
            <h2>Ad Campaigns</h2>
          </div>
          <span className="panel__meta">{campaigns.length} shown</span>
        </div>
        <DataTable
          caption="Advertising campaigns"
          columns={adColumns}
          emptyLabel="No advertising campaigns are configured."
          getRowKey={(row) => String(row.id)}
          rows={campaigns}
        />
      </section>
    </div>
  );
}
