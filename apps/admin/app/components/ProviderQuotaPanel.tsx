import { StatusBadge } from "./StatusBadge";

type QuotaWindow = {
  used: number;
  limit: number;
  remaining: number;
  reserve: number;
};

type ProviderQuota = {
  provider: string;
  status: string;
  lastSync: string;
  daily: QuotaWindow;
  minute: QuotaWindow;
  latencyMs: number;
  errorRate: string;
};

type ProviderQuotaPanelProps = {
  quota: ProviderQuota;
};

function QuotaMeter({ label, quota }: { label: string; quota: QuotaWindow }) {
  const usedPercent = Math.min((quota.used / quota.limit) * 100, 100);
  const reservePercent = Math.min((quota.reserve / quota.limit) * 100, 100);

  return (
    <div className="quota-meter">
      <div className="quota-meter__topline">
        <strong>{label}</strong>
        <span>
          {quota.remaining} remaining of {quota.limit}
        </span>
      </div>
      <div className="quota-meter__track">
        <span className="quota-meter__used" style={{ width: `${usedPercent}%` }} />
        <span className="quota-meter__reserve" style={{ width: `${reservePercent}%` }} />
      </div>
      <p>{quota.reserve} request reserve protected.</p>
    </div>
  );
}

export function ProviderQuotaPanel({ quota }: ProviderQuotaPanelProps) {
  return (
    <section className="panel">
      <div className="panel__header">
        <div>
          <p className="eyebrow">Provider</p>
          <h2>{quota.provider} Quota</h2>
        </div>
        <StatusBadge status={quota.status}>{quota.status}</StatusBadge>
      </div>
      <div className="quota-grid">
        <QuotaMeter label="Daily window" quota={quota.daily} />
        <QuotaMeter label="Minute window" quota={quota.minute} />
      </div>
      <dl className="inline-stats">
        <div>
          <dt>Last sync</dt>
          <dd>{quota.lastSync}</dd>
        </div>
        <div>
          <dt>Latency</dt>
          <dd>{quota.latencyMs} ms</dd>
        </div>
        <div>
          <dt>Error rate</dt>
          <dd>{quota.errorRate}</dd>
        </div>
      </dl>
    </section>
  );
}
