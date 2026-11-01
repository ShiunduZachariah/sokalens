import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Matches | SokaLens Admin",
  description: "SokaLens ingested match data.",
};

type AdminMatch = {
  id: number;
  league_name: string;
  home_team: string;
  away_team: string;
  kickoff_at: string;
  status: string;
  raw_status?: string;
  raw_status_label?: string;
  home_score?: number;
  away_score?: number;
  prediction_state?: string;
  prediction?: { market_key: string; selection: string; confidence: number } | null;
};

const matchColumns: DataTableColumn<AdminMatch>[] = [
  { key: "id", header: "ID" },
  { key: "home_team", header: "Home" },
  { key: "away_team", header: "Away" },
  { key: "league_name", header: "League", priority: "secondary" },
  { key: "kickoff_at", header: "Kickoff" },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge>,
  },
  {
    key: "home_score",
    header: "Score",
    render: (row) => `${row.home_score ?? "-"} - ${row.away_score ?? "-"}`,
  },
  {
    key: "prediction_state",
    header: "Prediction state",
    priority: "secondary",
    render: (row) => row.prediction_state ?? "—",
  },
];

export default async function Page() {
  const response = await adminApi<{ matches: AdminMatch[] }>("/v1/admin/matches?date=&limit=100");
  const matches = response?.matches ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Data</p>
          <h1>Matches</h1>
          <p>SokaLens ingested match data.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Ingested</p>
            <h2>Matches</h2>
          </div>
          <span className="panel__meta">{matches.length} shown</span>
        </div>
        <DataTable
          caption="Ingested matches"
          columns={matchColumns}
          emptyLabel="No matches are available."
          getRowKey={(row) => String(row.id)}
          rows={matches}
        />
      </section>
    </div>
  );
}
