type ErrorStateProps = {
  requestId?: string;
};

export function ErrorState({ requestId = "req_public_web" }: ErrorStateProps) {
  return (
    <div className="state-card" role="alert">
      <h2>We could not load this section.</h2>
      <p className="muted">Try again in a moment. Navigation and saved pages remain available.</p>
      <p className="micro">Request ID: {requestId}</p>
    </div>
  );
}
