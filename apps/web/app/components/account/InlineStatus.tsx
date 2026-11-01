type InlineStatusProps = {
  message: string;
  tone?: "error" | "info" | "success";
};

export function InlineStatus({ message, tone = "info" }: InlineStatusProps) {
  return (
    <div
      className={`inline-status inline-status--${tone}`}
      role={tone === "error" ? "alert" : "status"}
    >
      {message}
    </div>
  );
}
