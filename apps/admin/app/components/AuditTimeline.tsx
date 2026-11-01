import { StatusBadge } from "./StatusBadge";

type AuditEvent = {
  time: string;
  actor: string;
  action: string;
  entity: string;
  status: string;
  requestId: string;
};

type AuditTimelineProps = {
  events: AuditEvent[];
};

export function AuditTimeline({ events }: AuditTimelineProps) {
  return (
    <section className="panel">
      <div className="panel__header">
        <div>
          <p className="eyebrow">Audit</p>
          <h2>Recent Activity</h2>
        </div>
        <span className="panel__meta">Every write has actor and reason</span>
      </div>
      <ol className="audit-timeline">
        {events.map((event) => (
          <li key={`${event.time}-${event.requestId}`}>
            <time>{event.time}</time>
            <div>
              <strong>{event.action}</strong>
              <p>
                {event.actor} on {event.entity}
              </p>
              <code>{event.requestId}</code>
            </div>
            <StatusBadge status={event.status}>{event.status}</StatusBadge>
          </li>
        ))}
      </ol>
    </section>
  );
}
