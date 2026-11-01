import { DataTable, type DataTableColumn } from "../components/DataTable";
import { StatusBadge } from "../components/StatusBadge";
import { adminApi } from "../data/adminApi";

type JobRow = {
  id: number;
  job_key: string;
  queue: string;
  status: string;
  priority: number;
  attempts: number;
  max_attempts: number;
  created_at: string;
  completed_at: string | null;
  last_error: string;
};

const jobColumns: DataTableColumn<JobRow>[] = [
  { key: "id", header: "Job ID" },
  { key: "job_key", header: "Job" },
  { key: "queue", header: "Queue", priority: "secondary" },
  {
    key: "status",
    header: "Status",
    render: (row) => <StatusBadge status={row.status}>{row.status}</StatusBadge>,
  },
  { key: "priority", header: "Priority", align: "right" },
  { key: "attempts", header: "Attempts", align: "right" },
  { key: "created_at", header: "Created", priority: "secondary" },
  { key: "last_error", header: "Last error", priority: "secondary" },
];

export default async function JobsPage() {
  const response = await adminApi<{ jobs: JobRow[] }>("/v1/admin/jobs?limit=100");
  const jobs = response?.jobs ?? [];
  const failed = jobs.filter((job) => job.status === "failed" || job.status === "dead_letter");

  return (
    <div className="page-stack">
      <header className="page-header">
        <div>
          <p className="eyebrow">Workers</p>
          <h1>Jobs</h1>
          <p>Recent worker jobs from PostgreSQL, newest first.</p>
        </div>
      </header>

      <section className="panel">
        <div className="panel__header">
          <div>
            <p className="eyebrow">Queue</p>
            <h2>Recent Jobs</h2>
          </div>
          <span className="panel__meta">{jobs.length} shown · {failed.length} failed</span>
        </div>
        <DataTable
          caption="Recent jobs"
          columns={jobColumns}
          emptyLabel="No jobs are available."
          getRowKey={(row) => String(row.id)}
          rows={jobs}
        />
      </section>
    </div>
  );
}
