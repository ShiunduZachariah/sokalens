import type { Metadata } from "next";

import { DataTable, type DataTableColumn } from "../components/DataTable";
import { adminApi } from "../data/adminApi";

export const metadata: Metadata = {
  title: "Settings | SokaLens Admin",
  description: "SokaLens runtime configuration.",
};

type Setting = {
  key: string;
  value: unknown;
  updated_by: number;
  updated_at: string;
};

async function updateEmailProvider(formData: FormData) {
  "use server";
  const provider = String(formData.get("provider") ?? "");
  await adminApi("/v1/admin/email/provider", {
    method: "PUT",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ provider }),
  });
}

const settingColumns: DataTableColumn<Setting>[] = [
  { key: "key", header: "Key" },
  { key: "value", header: "Value", render: (row) => JSON.stringify(row.value ?? null) },
  { key: "updated_by", header: "Updated by", priority: "secondary" },
  { key: "updated_at", header: "Updated", priority: "secondary" },
];

export default async function Page() {
  const response = await adminApi<{ settings: Setting[] }>("/v1/admin/settings");
  const settings = response?.settings ?? [];

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Operations</p>
          <h1>Settings</h1>
          <p>SokaLens runtime configuration.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Runtime</p>
            <h2>Runtime Settings</h2>
          </div>
          <span className="panel__meta">{settings.length} shown</span>
        </div>
        <DataTable
          caption="Runtime settings"
          columns={settingColumns}
          emptyLabel="No runtime settings are stored."
          getRowKey={(row) => row.key}
          rows={settings}
        />
      </section>

      <section className="panel">
        <div className="panel__header"><div><p className="eyebrow">Transactional email</p><h2>Active Provider</h2></div></div>
        <form action={updateEmailProvider} className="form-grid">
          <label>Provider<select name="provider" defaultValue={String(settings.find((setting) => setting.key === "email.provider")?.value ?? "cloudflare")}><option value="cloudflare">Cloudflare</option><option value="resend">Resend fallback</option><option value="mailtrap">Mailtrap</option><option value="smtp">SMTP</option><option value="log">Log only</option></select></label>
          <button type="submit">Save provider</button>
        </form>
      </section>
    </div>
  );
}
