import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Policy | SokaLens Admin",
  description: "SokaLens geo and country policy.",
};

type RestrictionRule = {
  id: number;
  feature_key: string;
  action: string;
  starts_at: string;
  reason: string;
  country_code?: string;
  region_code?: string;
  platform?: string;
  placement_key?: string;
  ends_at?: string;
};

type FeatureFlag = {
  feature_key: string;
  enabled: boolean;
  default_action: string;
  description: string;
  updated_at: string;
};

const ruleColumns: DataTableColumn<RestrictionRule>[] = [
  { key: "id", header: "ID" },
  { key: "feature_key", header: "Feature" },
  { key: "action", header: "Action" },
  { key: "country_code", header: "Country", render: (row) => row.country_code ?? "—" },
  { key: "placement_key", header: "Placement", render: (row) => row.placement_key ?? "—" },
  { key: "starts_at", header: "Starts" },
  { key: "reason", header: "Reason" },
];

const flagColumns: DataTableColumn<FeatureFlag>[] = [
  { key: "feature_key", header: "Feature" },
  {
    key: "enabled",
    header: "Enabled",
    render: (row) => (
      <StatusBadge status={row.enabled ? "ok" : "error"}>{row.enabled ? "On" : "Off"}</StatusBadge>
    ),
  },
  { key: "default_action", header: "Default action" },
  { key: "description", header: "Description" },
];

export default async function Page() {
  const response = await adminApi<{ rules: RestrictionRule[]; feature_flags: FeatureFlag[] }>(
    "/v1/admin/policy/rules",
  );
  const rules = response?.rules ?? [];
  const featureFlags = response?.feature_flags ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Policy</p>
          <h1>Policy</h1>
          <p>SokaLens geo and country policy.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Rules</p>
            <h2>Restriction Rules</h2>
          </div>
          <span className="panel__meta">{rules.length} shown</span>
        </div>
        <DataTable
          caption="Restriction rules"
          columns={ruleColumns}
          emptyLabel="No restriction rules are configured."
          getRowKey={(row) => String(row.id)}
          rows={rules}
        />
      </section>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Flags</p>
            <h2>Feature Flags</h2>
          </div>
          <span className="panel__meta">{featureFlags.length} shown</span>
        </div>
        <DataTable
          caption="Feature flags"
          columns={flagColumns}
          emptyLabel="No feature flags are configured."
          getRowKey={(row) => row.feature_key}
          rows={featureFlags}
        />
      </section>
    </div>
  );
}
