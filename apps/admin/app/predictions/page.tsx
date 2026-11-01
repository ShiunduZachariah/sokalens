import Link from "next/link";
import type { Metadata } from "next";
import { DataTable, type DataTableColumn } from "../components/DataTable";
import { MetricCard } from "../components/MetricCard";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";
import { fetchLivePredictionRows, type LivePredictionRow } from "../data/livePredictions";

export const metadata: Metadata = { title: "Predictions | SokaLens Admin", description: "Live prediction performance and settled market results." };

type ResultRow = { prediction_id: number; match_id: number; match_title: string; league_name: string; market_key: string; selection: string; actual: string; outcome: string; confidence: number };
type HitRate = { settled: number; won: number; lost: number; void: number; hit_rate: number; markets: Array<{ market_key: string; settled: number; won: number; lost: number; void: number; hit_rate: number }> };

const resultColumns: DataTableColumn<ResultRow>[] = [
  { key: "match_title", header: "Match" },
  { key: "league_name", header: "League", priority: "secondary" },
  { key: "market_key", header: "Market" },
  { key: "selection", header: "Prediction" },
  { key: "actual", header: "Actual" },
  { key: "outcome", header: "Result", render: (row) => <StatusBadge status={row.outcome}>{row.outcome}</StatusBadge> },
  { key: "confidence", header: "Confidence", align: "right", render: (row) => `${Math.round(row.confidence)}%` },
];

const marketColumns: DataTableColumn<HitRate["markets"][number]>[] = [
  { key: "market_key", header: "Market" }, { key: "settled", header: "Settled", align: "right" },
  { key: "won", header: "Won", align: "right" }, { key: "lost", header: "Lost", align: "right" },
  { key: "void", header: "Void", align: "right" },
  { key: "hit_rate", header: "Success", align: "right", render: (row) => `${row.hit_rate.toFixed(1)}%` },
];

const publishedColumns: DataTableColumn<LivePredictionRow>[] = [
  { key: "match", header: "Match" },
  { key: "market", header: "Market" },
  { key: "pick", header: "Prediction" },
  { key: "confidence", header: "Confidence", align: "right" },
  { key: "model", header: "Source", render: (row) => <StatusBadge status={row.model === "provider" ? "warning" : "ready"}>{row.model}</StatusBadge> },
  { key: "status", header: "Publishing state", priority: "secondary" },
];

export default async function Page({ searchParams }: { searchParams?: Promise<Record<string, string | string[] | undefined>> }) {
  const requestedTab = (await searchParams)?.tab;
  const tab = requestedTab === "results" || requestedTab === "published" ? requestedTab : "performance";
  const [rate, results, published] = await Promise.all([
    adminApi<HitRate>("/v1/admin/predictions/hit-rate"),
    adminApi<{ results: ResultRow[]; total: number }>("/v1/admin/predictions/results?day=today&timezone=Africa%2FNairobi&limit=100"),
    fetchLivePredictionRows(100),
  ]);
  return <div className="page-stack">
    <header className="page-header"><div><p className="eyebrow">Predictions</p><h1>Prediction performance</h1><p>Live settlement data from the engine, grouped by market.</p></div></header>
    <nav className="admin-tabs" aria-label="Prediction views">
      <Link className={tab === "performance" ? "is-active" : ""} href="/predictions">Performance</Link>
      <Link className={tab === "published" ? "is-active" : ""} href="/predictions?tab=published">Published predictions</Link>
      <Link className={tab === "results" ? "is-active" : ""} href="/predictions?tab=results">Match results</Link>
    </nav>
    <section className="metric-grid">
      <MetricCard label="Overall success" value={`${(rate?.hit_rate ?? 0).toFixed(1)}%`} detail={`${rate?.won ?? 0} won · ${rate?.lost ?? 0} lost`} status="ready" />
      <MetricCard label="Settled markets" value={String(rate?.settled ?? 0)} detail={`${rate?.void ?? 0} void`} status="ready" />
      <MetricCard label="Today's results" value={String(results?.total ?? 0)} detail="Africa/Nairobi local day" status="ready" />
    </section>
    {tab === "published" ? <section className="panel"><div className="panel__header"><div><p className="eyebrow">Live publishing stream</p><h2>Engine and provider predictions</h2></div><span className="panel__meta">{published.length} shown</span></div>
      <DataTable caption="Currently published predictions" columns={publishedColumns} emptyLabel="No published predictions are available." getRowKey={(row) => `${row.match}:${row.market}:${row.pick}:${row.model}`} rows={published} />
    </section> : tab === "results" ? <section className="panel"><div className="panel__header"><div><p className="eyebrow">Settlement</p><h2>Predicted markets and results</h2></div><span className="panel__meta">{results?.total ?? 0} total</span></div>
      <DataTable caption="Today's settled predictions" columns={resultColumns} emptyLabel="No settled predictions for today." getRowKey={(row) => String(row.prediction_id)} rows={results?.results ?? []} />
    </section> : <section className="panel"><div className="panel__header"><div><p className="eyebrow">Accuracy</p><h2>Performance by market</h2></div></div>
      <DataTable caption="Prediction hit rate by market" columns={marketColumns} emptyLabel="No settlement statistics are available yet." getRowKey={(row) => row.market_key} rows={rate?.markets ?? []} />
    </section>}
  </div>;
}
