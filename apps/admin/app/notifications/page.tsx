import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Notifications | SokaLens Admin",
  description: "SokaLens notification delivery.",
};

type NotificationRow = {
  id: number;
  user_id: number;
  category: string;
  title: string;
  body: string;
  action_url: string;
  entity_type: string;
  entity_id: string;
  read_at: string | null;
  created_at: string;
  expires_at: string | null;
};

type OutboxRow = {
  id: number;
  user_id: number;
  notification_id: number | null;
  channel: string;
  category: string;
  priority: number;
  status: string;
  attempts: number;
  max_attempts: number;
  last_error: string;
  sent_at: string | null;
  created_at: string;
};

const notificationColumns: DataTableColumn<NotificationRow>[] = [
  { key: "id", header: "ID" },
  { key: "user_id", header: "User" },
  { key: "category", header: "Category" },
  { key: "title", header: "Title" },
  { key: "read_at", header: "Read", render: (row) => row.read_at ?? "Unread" },
  { key: "created_at", header: "Created" },
];

const outboxColumns: DataTableColumn<OutboxRow>[] = [
  { key: "id", header: "ID" },
  { key: "channel", header: "Channel" },
  { key: "category", header: "Category" },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge>,
  },
  { key: "priority", header: "Priority", align: "right" },
  { key: "attempts", header: "Attempts", align: "right" },
  { key: "last_error", header: "Last error" },
  { key: "created_at", header: "Created" },
];

export default async function Page() {
  const response = await adminApi<{ notifications: NotificationRow[]; outbox: OutboxRow[] }>(
    "/v1/admin/notifications?limit=100",
  );
  const notifications = response?.notifications ?? [];
  const outbox = response?.outbox ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Engagement</p>
          <h1>Notifications</h1>
          <p>SokaLens notification delivery.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Inbox</p>
            <h2>Recent Notifications</h2>
          </div>
          <span className="panel__meta">{notifications.length} shown</span>
        </div>
        <DataTable
          caption="Recent notifications"
          columns={notificationColumns}
          emptyLabel="No notifications have been created."
          getRowKey={(row) => String(row.id)}
          rows={notifications}
        />
      </section>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Queue</p>
            <h2>Outbox Queue</h2>
          </div>
          <span className="panel__meta">{outbox.length} shown</span>
        </div>
        <DataTable
          caption="Outbox queue"
          columns={outboxColumns}
          emptyLabel="The outbox is empty."
          getRowKey={(row) => String(row.id)}
          rows={outbox}
        />
      </section>
    </div>
  );
}
