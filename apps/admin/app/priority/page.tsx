import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Priority | SokaLens Admin",
  description: "League priority scoring.",
};

type LeagueRow = {
  id: number;
  slug: string;
  name: string;
  country: string;
  sport: string;
  priority_score: number;
};

const leagueColumns: DataTableColumn<LeagueRow>[] = [
  { key: "name", header: "League" },
  { key: "country", header: "Country" },
  { key: "sport", header: "Sport", priority: "secondary" },
  { key: "priority_score", header: "Priority score", align: "right" },
];

export default async function Page() {
  const response = await adminApi<{ leagues: LeagueRow[] }>("/v1/admin/leagues/priority?limit=200");
  const leagues = [...(response?.leagues ?? [])].sort(
    (a, b) => b.priority_score - a.priority_score,
  );

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Ranking</p>
          <h1>Priority</h1>
          <p>League priority scoring.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Ranking</p>
            <h2>League Priority</h2>
          </div>
          <span className="panel__meta">{leagues.length} shown, highest first</span>
        </div>
        <DataTable
          caption="League priority scoring"
          columns={leagueColumns}
          emptyLabel="No leagues are available."
          getRowKey={(row) => String(row.id)}
          rows={leagues}
        />
      </section>
    </div>
  );
}
