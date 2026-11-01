type EmptyStateProps = {
  title: string;
  message: string;
  actionLabel?: string;
  actionHref?: string;
};

export function EmptyState({ title, message, actionLabel, actionHref }: EmptyStateProps) {
  return (
    <div className="state-card">
      <h2>{title}</h2>
      <p className="muted">{message}</p>
      {actionHref && actionLabel ? (
        <a className="button button--secondary" href={actionHref}>
          {actionLabel}
        </a>
      ) : null}
    </div>
  );
}
