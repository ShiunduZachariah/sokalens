type CompetitionPillProps = {
  name: string;
  country?: string;
};

export function CompetitionPill({ name, country }: CompetitionPillProps) {
  return (
    <span className="pill pill--pitch">
      {name || "Competition"}
      {country ? `, ${country}` : ""}
    </span>
  );
}
