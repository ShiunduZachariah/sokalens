import { TeamCrest } from "./TeamCrest";

type TeamIdentityProps = {
  name: string;
  logo: string;
  /** Card default; a prediction row passes 20 (review 2.1). */
  size?: 16 | 20 | 24 | 32 | 44;
};

// TeamIdentity is the shared team rendering primitive: a logo (with a
// generated-crest fallback) beside the team name, in the standard team-line
// layout used across match/team/prediction surfaces.
export function TeamIdentity({ name, logo, size = 24 }: TeamIdentityProps) {
  return (
    <div className="team-line">
      <TeamCrest logoUrl={logo} name={name} size={size} />
      <span className="team-name">{name}</span>
    </div>
  );
}
