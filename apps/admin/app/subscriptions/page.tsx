import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";
import { PlanEditor, type PlanConfiguration } from "./PlanEditor";

type SubscriptionRow = {
  id: number;
  email: string;
  plan_key: string;
  status: string;
  provider: string;
  provider_subscription_id: string;
  current_period_end?: string;
  cancel_at_period_end: boolean;
  latest_webhook_status: string;
  open_disputes: number;
};

const subscriptionColumns: DataTableColumn<SubscriptionRow>[] = [
  { key: "email", header: "Account" },
  { key: "plan_key", header: "Plan" },
  { key: "status", header: "Status", render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge> },
  { key: "provider", header: "Provider", priority: "secondary" },
  { key: "provider_subscription_id", header: "Provider ref", priority: "secondary" },
  { key: "current_period_end", header: "Period end", render: (row) => row.current_period_end ? new Date(row.current_period_end).toLocaleString() : "Not set" },
  { key: "cancel_at_period_end", header: "Renewal", render: (row) => row.cancel_at_period_end ? "Stops at period end" : "Provider managed" },
  { key: "latest_webhook_status", header: "Webhook" },
  { key: "open_disputes", header: "Open disputes", render: (row) => String(row.open_disputes) },
];

export default async function SubscriptionsPage() {
  const [response, planResponse] = await Promise.all([
    adminApi<{ subscriptions: SubscriptionRow[] }>("/v1/admin/subscriptions?limit=200"),
    adminApi<{ plans: PlanConfiguration[] }>("/v1/admin/plans"),
  ]);
  return (
    <div className="page-stack">
      <header className="page-header"><div><p className="eyebrow">Revenue</p><h1>Subscriptions</h1><p>Provider-authoritative billing periods, verified webhook state, and disputes requiring review.</p></div></header>
      <section className="panel">
        <div className="panel__header"><div><p className="eyebrow">Billing</p><h2>Subscription state</h2></div><span className="panel__meta">Read-only review; corrections require audited workflows</span></div>
        <DataTable caption="Subscriptions" columns={subscriptionColumns} emptyLabel={response ? "No subscriptions are recorded." : "Billing data is unavailable or this session lacks permission."} getRowKey={(row) => String(row.id)} rows={response?.subscriptions ?? []} />
      </section>
      <PlanEditor plans={planResponse?.plans ?? []} />
    </div>
  );
}
