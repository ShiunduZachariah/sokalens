import { StatusBadge } from "./StatusBadge";

type QueueDepth = {
  priority: string;
  label: string;
  depth: number;
  running: number;
  oldest: string;
  status: string;
};

type QueueDepthPanelProps = {
  queues: QueueDepth[];
};

export function QueueDepthPanel({ queues }: QueueDepthPanelProps) {
  const maxDepth = Math.max(...queues.map((queue) => queue.depth), 1);

  return (
    <section className="panel">
      <div className="panel__header">
        <div>
          <p className="eyebrow">Queues</p>
          <h2>Queue Depth By Priority</h2>
        </div>
        <span className="panel__meta">Worker ingestion only</span>
      </div>
      <div className="queue-list">
        {queues.map((queue) => (
          <div className="queue-row" key={queue.priority}>
            <div className="queue-row__label">
              <strong>{queue.priority}</strong>
              <span>{queue.label}</span>
            </div>
            <div className="queue-row__bar" aria-label={`${queue.priority} depth ${queue.depth}`}>
              <span style={{ width: `${Math.max((queue.depth / maxDepth) * 100, 4)}%` }} />
            </div>
            <div className="queue-row__stats">
              <span>{queue.depth} queued</span>
              <span>{queue.running} running</span>
              <span>{queue.oldest} oldest</span>
              <StatusBadge status={queue.status}>{queue.status}</StatusBadge>
            </div>
          </div>
        ))}
      </div>
    </section>
  );
}
