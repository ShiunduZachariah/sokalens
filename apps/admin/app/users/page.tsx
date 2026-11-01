import { adminApi } from "../data/adminApi";
import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { UserActions } from "./UserActions";

type UserRow = {
  id: number;
  email: string;
  role: string;
  account_status: string;
  tier: string;
  email_verified: boolean;
  created_at: string;
  last_login_at?: string;
  actions: string;
};

const userColumns: DataTableColumn<UserRow>[] = [
  { key: "email", header: "User" },
  { key: "role", header: "Role" },
  { key: "tier", header: "Access" },
  { key: "email_verified", header: "Verified", render: (row) => row.email_verified ? "Yes" : "No" },
  { key: "account_status", header: "Status", render: (row) => <StatusBadge status={row.account_status}>{row.account_status}</StatusBadge> },
  { key: "created_at", header: "Created", priority: "secondary", render: (row) => new Date(row.created_at).toLocaleString() },
  { key: "last_login_at", header: "Last sign-in", priority: "secondary", render: (row) => row.last_login_at ? new Date(row.last_login_at).toLocaleString() : "Never" },
  { key: "actions", header: "Actions", render: (row) => <UserActions status={row.account_status} userId={row.id} /> },
];

export default async function UsersPage() {
  const response = await adminApi<{ users: Omit<UserRow, "actions">[] }>("/v1/admin/users?limit=100");
  const users = (response?.users ?? []).map((user) => ({ ...user, actions: "" }));
  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Accounts</p>
          <h1>Users</h1>
          <p>Live, permission-filtered account state. Suspensions require a reason and revoke active sessions.</p>
        </div>
      </header>
      <section className="panel">
        <div className="panel__header"><div><p className="eyebrow">Directory</p><h2>User list</h2></div><span className="panel__meta">Maximum 100 rows per request</span></div>
        <DataTable caption="Users" columns={userColumns} emptyLabel={response ? "No users match this view." : "User data is unavailable or this session lacks permission."} getRowKey={(row) => String(row.id)} rows={users} />
      </section>
    </div>
  );
}
