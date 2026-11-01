// Human labels for search entity types (team/league/country/match/player).
export function entityTypeLabel(type: string): string {
  switch (type) {
    case "team": return "Team";
    case "league": return "League";
    case "country": return "Country";
    case "match": return "Match";
    case "player": return "Player";
    case "prediction": return "Prediction";
    case "acca": return "ACCA";
    default: return type;
  }
}
