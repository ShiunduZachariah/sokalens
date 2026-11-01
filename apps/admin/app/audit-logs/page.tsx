import { adminApi } from "../data/adminApi";
import { DataTable, type DataTableColumn } from "../components/DataTable";

type AuditRow = {
  id: number;
  created_at: string;
  actor_user_id?: number;
  action: string;
  entity_type: string;
  entity_id?: string;
  reason?: string;
  before?: unknown;
  after?: unknown;
};

const columns: DataTableColumn<AuditRow>[] = [
  { key: "created_at", header: "Time", render: (row) => new Date(row.created_at).toLocaleString() },
  { key: "actor_user_id", header: "Actor" },
  { key: "action", header: "Action" },
  { key: "entity_type", header: "Entity", render: (row) => `${row.entity_type}${row.entity_id ? ` #${row.entity_id}` : ""}` },
  { key: "reason", header: "Reason", priority: "secondary" },
  { key: "before", header: "Before", priority: "optional", render: (row) => JSON.stringify(row.before ?? null) },
  { key: "after", header: "After", priority: "optional", render: (row) => JSON.stringify(row.after ?? null) },
];

export default async function AuditLogsPage() {
  const response = await adminApi<{ events: AuditRow[] }>("/v1/admin/audit-logs?limit=200");
  return <div className="page-stack">
    <header className="page-header"><div><p className="eyebrow">Traceability</p><h1>Audit logs</h1><p>Live append-only evidence for privileged account and policy actions.</p></div></header>
    <section className="panel"><div className="panel__header"><div><p className="eyebrow">History</p><h2>Recent events</h2></div><span className="panel__meta">Maximum 200 rows per request</span></div>
      <DataTable caption="Audit logs" columns={columns} emptyLabel={response ? "No audit events are available." : "Audit data is unavailable or this session lacks permission."} getRowKey={(row) => String(row.id)} rows={response?.events ?? []} />
    </section>
  </div>;
}
