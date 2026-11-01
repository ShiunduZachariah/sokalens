type ResponsibleNoteProps = {
  compact?: boolean;
};

export function ResponsibleNote({ compact = false }: ResponsibleNoteProps) {
  return (
    <aside className="responsible-note" aria-label="Responsible analysis note">
      <strong>Use predictions as analysis.</strong>
      <span>
        Predictions are informational and not guaranteed.{" "}
        {compact ? "Check the data context before making decisions." : "They should not be treated as financial advice, and outcomes can change with team news, market movement, or match conditions."}
      </span>
    </aside>
  );
}
