import { DataTable, type DataTableColumn } from "../components/DataTable";
import { adminApi } from "../data/adminApi";

type ActivityRow = {
  id: number;
  user_id?: number;
  email: string;
  event_type: string;
  country_code: string;
  platform: string;
  source: string;
  occurred_at: string;
  metadata: unknown;
};

function activityLabel(value: string) {
  const labels: Record<string, string> = {
    EMAIL_VERIFIED: "Verified email",
    FIRST_FAVORITE_ADDED: "Saved first favourite",
    USER_REGISTERED: "Created account",
  };
  return labels[value] ?? value.toLowerCase().replaceAll("_", " ").replace(/^./, (letter) => letter.toUpperCase());
}

const columns: DataTableColumn<ActivityRow>[] = [
  { key: "occurred_at", header: "Time", render: (row) => new Date(row.occurred_at).toLocaleString() },
  { key: "email", header: "User", render: (row) => row.email || (row.user_id ? `User #${row.user_id}` : "Anonymous") },
  { key: "event_type", header: "Action", render: (row) => activityLabel(row.event_type) },
  { key: "country_code", header: "Country", priority: "secondary", render: (row) => row.country_code || "Unknown" },
  { key: "platform", header: "Platform", priority: "secondary" },
  { key: "source", header: "Source", priority: "secondary", render: (row) => row.source || "Direct" },
  { key: "metadata", header: "Details", priority: "optional", render: (row) => JSON.stringify(row.metadata ?? {}) },
];

export default async function UserActivityPage() {
  const response = await adminApi<{ events: ActivityRow[] }>("/v1/admin/user-activity?limit=200");
  const events = response?.events ?? [];
  return <div className="page-stack">
    <header className="page-header"><div><p className="eyebrow">Accounts</p><h1>User activity</h1><p>Recent account creation, email verification, and first-favourite activation events.</p></div></header>
    <section className="panel"><div className="panel__header"><div><p className="eyebrow">Product journey</p><h2>Recent user actions</h2></div><span className="panel__meta">{events.length} shown, newest first</span></div>
      <DataTable caption="Recent user activity" columns={columns} emptyLabel={response ? "No user activity has been recorded yet." : "User activity is unavailable or this session lacks permission."} getRowKey={(row) => String(row.id)} rows={events} />
    </section>
  </div>;
}
