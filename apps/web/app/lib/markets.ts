// Single canonical presentation mapper for prediction markets and outcomes.
// Never interpolate API enum strings directly into UI text; route everything
// through formatMarket / formatOutcome so the public product reads cleanly.

import type { Locale } from "../i18n/config";

const marketLabels: Record<string, string> = {
  "1x2": "Match Result",
  match_result: "Match Result",
  double_chance: "Double Chance",
  draw_no_bet: "Draw No Bet",
  btts: "Both Teams to Score",
  correct_score: "Correct Score",
  total_goals: "Total Goals",
  team_to_score_first: "Team to Score First",
  highest_scoring_half: "Highest Scoring Half",
  odd_even_total_goals: "Odd/Even Goals",
  half_time_result: "Half-Time Result",
  ht_ft: "Half-Time/Full-Time",
  btts_total_goals: "BTTS & Total Goals",
  btts_match_result: "BTTS & Match Result",
  result_total_goals: "Result & Total Goals",
  team_ou_goals: "Team Goals",
  corners: "Corners",
  cards: "Cards",
  total_corners: "Total Corners",
  team_corners_home: "Home Team Corners",
  team_corners_away: "Away Team Corners",
  total_cards: "Total Cards",
  total_fouls: "Total Fouls",
  total_shots: "Total Shots",
  shots_on_target: "Shots on Target",
  possession_winner: "Possession Winner",
  fouls: "Fouls",
  throw_ins: "Throw-Ins",
  team_over_under_goals: "Team Goals",
  halftime_fulltime: "Half-Time/Full-Time",
};

const localizedTerms: Record<Locale, Record<string, string>> = {
  en: {},
  es: {
    "BTTS": "Ambos marcan", "Prediction": "Predicción", "Match Result": "Resultado del partido", "Double Chance": "Doble oportunidad", "Draw No Bet": "Empate no válido", "Both Teams to Score": "Ambos equipos marcan", "Correct Score": "Marcador exacto", "Total Goals": "Total de goles", "Team to Score First": "Primer equipo en marcar", "Highest Scoring Half": "Parte con más goles", "Odd/Even Goals": "Goles pares/impares", "Half-Time Result": "Resultado al descanso", "Half-Time/Full-Time": "Descanso/Final", "BTTS & Total Goals": "Ambos marcan y total de goles", "BTTS & Match Result": "Ambos marcan y resultado", "Result & Total Goals": "Resultado y total de goles", "Team Goals": "Goles del equipo", "Corners": "Córners", "Cards": "Tarjetas", "Total Corners": "Total de córners", "Home Team Corners": "Córners del equipo local", "Away Team Corners": "Córners del equipo visitante", "Total Cards": "Total de tarjetas", "Total Fouls": "Total de faltas", "Total Shots": "Total de tiros", "Shots on Target": "Tiros a puerta", "Possession Winner": "Ganador de posesión", "Fouls": "Faltas", "Throw-Ins": "Saques de banda", "Home": "Local", "Draw": "Empate", "Away": "Visitante", "Home or Draw": "Local o empate", "Home or Away": "Local o visitante", "Draw or Away": "Empate o visitante", "Yes": "Sí", "No": "No", "Even": "Par", "Odd": "Impar", "Equal": "Igual", "First Half": "Primera parte", "Second Half": "Segunda parte", "No Goals": "Sin goles", "Home More": "Más el local", "Away More": "Más el visitante", "Home Yes": "Local: sí", "Away Yes": "Visitante: sí", "Over": "Más de", "Under": "Menos de", "Goals": "goles", "Shots": "tiros"
  },
  fr: {
    "BTTS": "Les deux marquent", "Prediction": "Prédiction", "Match Result": "Résultat du match", "Double Chance": "Double chance", "Draw No Bet": "Remboursé si nul", "Both Teams to Score": "Les deux équipes marquent", "Correct Score": "Score exact", "Total Goals": "Total de buts", "Team to Score First": "Première équipe à marquer", "Highest Scoring Half": "Mi-temps la plus prolifique", "Odd/Even Goals": "Buts pairs/impairs", "Half-Time Result": "Résultat à la mi-temps", "Half-Time/Full-Time": "Mi-temps/Fin du match", "BTTS & Total Goals": "Les deux marquent et total de buts", "BTTS & Match Result": "Les deux marquent et résultat", "Result & Total Goals": "Résultat et total de buts", "Team Goals": "Buts de l’équipe", "Corners": "Corners", "Cards": "Cartons", "Total Corners": "Total de corners", "Home Team Corners": "Corners de l’équipe à domicile", "Away Team Corners": "Corners de l’équipe à l’extérieur", "Total Cards": "Total de cartons", "Total Fouls": "Total de fautes", "Total Shots": "Total de tirs", "Shots on Target": "Tirs cadrés", "Possession Winner": "Équipe avec le plus de possession", "Fouls": "Fautes", "Throw-Ins": "Touches", "Home": "Domicile", "Draw": "Nul", "Away": "Extérieur", "Home or Draw": "Domicile ou nul", "Home or Away": "Domicile ou extérieur", "Draw or Away": "Nul ou extérieur", "Yes": "Oui", "No": "Non", "Even": "Pair", "Odd": "Impair", "Equal": "Égalité", "First Half": "Première mi-temps", "Second Half": "Deuxième mi-temps", "No Goals": "Aucun but", "Home More": "Domicile, davantage", "Away More": "Extérieur, davantage", "Home Yes": "Domicile : oui", "Away Yes": "Extérieur : oui", "Over": "Plus de", "Under": "Moins de", "Goals": "buts", "Shots": "tirs"
  },
  pt: {
    "BTTS": "Ambas marcam", "Prediction": "Previsão", "Match Result": "Resultado do jogo", "Double Chance": "Dupla hipótese", "Draw No Bet": "Empate anula", "Both Teams to Score": "Ambas as equipas marcam", "Correct Score": "Resultado exato", "Total Goals": "Total de golos", "Team to Score First": "Primeira equipa a marcar", "Highest Scoring Half": "Parte com mais golos", "Odd/Even Goals": "Golos pares/ímpares", "Half-Time Result": "Resultado ao intervalo", "Half-Time/Full-Time": "Intervalo/Final", "BTTS & Total Goals": "Ambas marcam e total de golos", "BTTS & Match Result": "Ambas marcam e resultado", "Result & Total Goals": "Resultado e total de golos", "Team Goals": "Golos da equipa", "Corners": "Cantos", "Cards": "Cartões", "Total Corners": "Total de cantos", "Home Team Corners": "Cantos da equipa da casa", "Away Team Corners": "Cantos da equipa visitante", "Total Cards": "Total de cartões", "Total Fouls": "Total de faltas", "Total Shots": "Total de remates", "Shots on Target": "Remates à baliza", "Possession Winner": "Vencedor da posse de bola", "Fouls": "Faltas", "Throw-Ins": "Lançamentos laterais", "Home": "Casa", "Draw": "Empate", "Away": "Fora", "Home or Draw": "Casa ou empate", "Home or Away": "Casa ou fora", "Draw or Away": "Empate ou fora", "Yes": "Sim", "No": "Não", "Even": "Par", "Odd": "Ímpar", "Equal": "Igual", "First Half": "Primeira parte", "Second Half": "Segunda parte", "No Goals": "Sem golos", "Home More": "Mais a equipa da casa", "Away More": "Mais a equipa visitante", "Home Yes": "Casa: sim", "Away Yes": "Fora: sim", "Over": "Mais de", "Under": "Menos de", "Goals": "golos", "Shots": "remates"
  },
  ar: {
    "BTTS": "يسجل الفريقان", "Prediction": "التوقع", "Match Result": "نتيجة المباراة", "Double Chance": "فرصة مزدوجة", "Draw No Bet": "التعادل يعيد الرهان", "Both Teams to Score": "يسجل الفريقان", "Correct Score": "النتيجة الصحيحة", "Total Goals": "مجموع الأهداف", "Team to Score First": "الفريق الذي يسجل أولاً", "Highest Scoring Half": "الشوط الأكثر تسجيلاً", "Odd/Even Goals": "أهداف فردية/زوجية", "Half-Time Result": "نتيجة الشوط الأول", "Half-Time/Full-Time": "الشوط الأول/نهاية المباراة", "BTTS & Total Goals": "يسجل الفريقان ومجموع الأهداف", "BTTS & Match Result": "يسجل الفريقان ونتيجة المباراة", "Result & Total Goals": "النتيجة ومجموع الأهداف", "Team Goals": "أهداف الفريق", "Corners": "الركنيات", "Cards": "البطاقات", "Total Corners": "مجموع الركنيات", "Home Team Corners": "ركنيات صاحب الأرض", "Away Team Corners": "ركنيات الضيف", "Total Cards": "مجموع البطاقات", "Total Fouls": "مجموع الأخطاء", "Total Shots": "مجموع التسديدات", "Shots on Target": "التسديدات على المرمى", "Possession Winner": "الأكثر استحواذاً", "Fouls": "الأخطاء", "Throw-Ins": "رميات التماس", "Home": "صاحب الأرض", "Draw": "تعادل", "Away": "الضيف", "Home or Draw": "صاحب الأرض أو تعادل", "Home or Away": "صاحب الأرض أو الضيف", "Draw or Away": "تعادل أو الضيف", "Yes": "نعم", "No": "لا", "Even": "زوجي", "Odd": "فردي", "Equal": "متساوي", "First Half": "الشوط الأول", "Second Half": "الشوط الثاني", "No Goals": "دون أهداف", "Home More": "صاحب الأرض أكثر", "Away More": "الضيف أكثر", "Home Yes": "صاحب الأرض: نعم", "Away Yes": "الضيف: نعم", "Over": "أكثر من", "Under": "أقل من", "Goals": "أهداف", "Shots": "تسديدات"
  },
  de: {
    "BTTS": "Beide treffen", "Prediction": "Prognose", "Match Result": "Spielergebnis", "Double Chance": "Doppelte Chance", "Draw No Bet": "Unentschieden ohne Wette", "Both Teams to Score": "Beide Teams treffen", "Correct Score": "Genaues Ergebnis", "Total Goals": "Tore gesamt", "Team to Score First": "Team erzielt das erste Tor", "Highest Scoring Half": "Torreichste Halbzeit", "Odd/Even Goals": "Ungerade/Gerade Tore", "Half-Time Result": "Halbzeitergebnis", "Half-Time/Full-Time": "Halbzeit/Endstand", "BTTS & Total Goals": "Beide treffen und Tore gesamt", "BTTS & Match Result": "Beide treffen und Spielergebnis", "Result & Total Goals": "Ergebnis und Tore gesamt", "Team Goals": "Teamtore", "Corners": "Ecken", "Cards": "Karten", "Total Corners": "Ecken gesamt", "Home Team Corners": "Ecken Heimteam", "Away Team Corners": "Ecken Auswärtsteam", "Total Cards": "Karten gesamt", "Total Fouls": "Fouls gesamt", "Total Shots": "Schüsse gesamt", "Shots on Target": "Torschüsse", "Possession Winner": "Ballbesitz-Sieger", "Fouls": "Fouls", "Throw-Ins": "Einwürfe", "Home": "Heim", "Draw": "Unentschieden", "Away": "Auswärts", "Home or Draw": "Heim oder Unentschieden", "Home or Away": "Heim oder Auswärts", "Draw or Away": "Unentschieden oder Auswärts", "Yes": "Ja", "No": "Nein", "Even": "Gerade", "Odd": "Ungerade", "Equal": "Gleich", "First Half": "Erste Halbzeit", "Second Half": "Zweite Halbzeit", "No Goals": "Keine Tore", "Home More": "Heim mehr", "Away More": "Auswärts mehr", "Home Yes": "Heim: Ja", "Away Yes": "Auswärts: Ja", "Over": "Über", "Under": "Unter", "Goals": "Tore", "Shots": "Schüsse"
  },
  it: {
    "BTTS": "Entrambe segnano", "Prediction": "Pronostico", "Match Result": "Esito partita", "Double Chance": "Doppia chance", "Draw No Bet": "Pareggio rimborsato", "Both Teams to Score": "Entrambe le squadre segnano", "Correct Score": "Risultato esatto", "Total Goals": "Totale gol", "Team to Score First": "Prima squadra a segnare", "Highest Scoring Half": "Tempo con più gol", "Odd/Even Goals": "Gol pari/dispari", "Half-Time Result": "Risultato primo tempo", "Half-Time/Full-Time": "Primo tempo/Finale", "BTTS & Total Goals": "Entrambe segnano e totale gol", "BTTS & Match Result": "Entrambe segnano ed esito partita", "Result & Total Goals": "Esito e totale gol", "Team Goals": "Gol della squadra", "Corners": "Calci d’angolo", "Cards": "Cartellini", "Total Corners": "Totale calci d’angolo", "Home Team Corners": "Calci d’angolo squadra di casa", "Away Team Corners": "Calci d’angolo squadra ospite", "Total Cards": "Totale cartellini", "Total Fouls": "Totale falli", "Total Shots": "Totale tiri", "Shots on Target": "Tiri in porta", "Possession Winner": "Vincitore possesso palla", "Fouls": "Falli", "Throw-Ins": "Rimesse laterali", "Home": "Casa", "Draw": "Pareggio", "Away": "Trasferta", "Home or Draw": "Casa o pareggio", "Home or Away": "Casa o trasferta", "Draw or Away": "Pareggio o trasferta", "Yes": "Sì", "No": "No", "Even": "Pari", "Odd": "Dispari", "Equal": "Uguale", "First Half": "Primo tempo", "Second Half": "Secondo tempo", "No Goals": "Nessun gol", "Home More": "Casa di più", "Away More": "Trasferta di più", "Home Yes": "Casa: sì", "Away Yes": "Trasferta: sì", "Over": "Più di", "Under": "Meno di", "Goals": "gol", "Shots": "tiri"
  },
};

function localizeTerm(term: string, locale: Locale): string {
  return localizedTerms[locale]?.[term] ?? term;
}

function localizeUnit(term: string, locale: Locale): string {
  const localized = localizeTerm(term, locale);
  return (["es", "fr", "pt", "it"] as Locale[]).includes(locale)
    ? localized.charAt(0).toLocaleLowerCase(locale) + localized.slice(1)
    : localized;
}

const outcomeLabels: Record<string, string> = {
  "1": "Home",
  x: "Draw",
  "2": "Away",
  home: "Home",
  draw: "Draw",
  away: "Away",
  home_draw: "Home or Draw",
  home_away: "Home or Away",
  draw_away: "Draw or Away",
  home_or_draw: "Home or Draw",
  draw_or_away: "Draw or Away",
  home_or_away: "Home or Away",
  yes: "Yes",
  no: "No",
  even: "Even",
  odd: "Odd",
  equal: "Equal",
  first_half: "First Half",
  second_half: "Second Half",
  no_goals: "No Goals",
  home_more: "Home More",
  away_more: "Away More",
  home_yes: "Home Yes",
  away_yes: "Away Yes",
};

const halfTimeFullTimeLabels: Record<string, string> = {
  ht_home_ft_home: "Home / Home",
  ht_home_ft_draw: "Home / Draw",
  ht_home_ft_away: "Home / Away",
  ht_draw_ft_home: "Draw / Home",
  ht_draw_ft_draw: "Draw / Draw",
  ht_draw_ft_away: "Draw / Away",
  ht_away_ft_home: "Away / Home",
  ht_away_ft_draw: "Away / Draw",
  ht_away_ft_away: "Away / Away",
};

// Markets that must not surface in the public product. SokaLens-engine
// correct-score is high-variance and can contradict broader markets, so it is
// suppressed; only an explicitly approved provider correct-score may return later.
export const HIDDEN_PUBLIC_MARKETS = new Set(["correct_score"]);

// humanize is the last-resort fallback that turns an unknown snake_case key
// into readable title case, so a genuinely new enum never leaks raw.
function humanize(value: string): string {
  const words = value
    .replace(/([a-z])([0-9])/g, "$1 $2")
    .replace(/([0-9])([a-z])/g, "$1 $2")
    .replace(/_/g, " ")
    .replace(/\s+/g, " ")
    .trim()
    .split(" ");
  return words
    .map((word) => (word ? word[0].toUpperCase() + word.slice(1) : word))
    .join(" ");
}

// over/under line labels: over_2.5 -> "Over 2.5 Goals", under_0.5 -> "Under 0.5 Goals".
const overUnderLine = /^(over|under)_(\d+(?:\.\d+)?)$/;
// yes + line: yes_over_2.5 -> "Yes & Over 2.5 Goals".
const yesLine = /^(yes)_(over|under)_(\d+(?:\.\d+)?)$/;
// team-scoped line: home_over_1.5 -> "Home Over 1.5 Goals".
const teamLine = /^(home|away)_(over|under)_(\d+(?:\.\d+)?)$/;

const marketLineUnits: Record<string, string> = {
  total_goals: "Goals",
  team_ou_goals: "Goals",
  team_over_under_goals: "Goals",
  total_corners: "Corners",
  team_corners_home: "Corners",
  team_corners_away: "Corners",
  total_cards: "Cards",
  total_fouls: "Fouls",
  total_shots: "Shots",
  shots_on_target: "Shots on Target",
};

export function formatMarket(marketKey: string, locale: Locale = "en"): string {
  const key = String(marketKey ?? "").trim().toLowerCase();
  if (!key) return localizeTerm("Prediction", locale);
  return localizeTerm(marketLabels[key] ?? humanize(key), locale);
}

export function formatOutcome(selection: string, locale: Locale = "en"): string {
  const key = String(selection ?? "").trim().toLowerCase();
  if (!key) return "—";
  if (outcomeLabels[key]) return localizeTerm(outcomeLabels[key], locale);
  if (halfTimeFullTimeLabels[key]) {
    const [, halfTime, fullTime] = /^ht_(home|draw|away)_ft_(home|draw|away)$/.exec(key) ?? [];
    return halfTime && fullTime
      ? `${localizeTerm(outcomeLabels[halfTime], locale)} / ${localizeTerm(outcomeLabels[fullTime], locale)}`
      : halfTimeFullTimeLabels[key];
  }
  const team = teamLine.exec(key);
  if (team) {
    return `${localizeTerm(humanize(team[1]), locale)} ${localizeTerm(humanize(team[2]), locale)} ${team[3]} ${localizeTerm("Goals", locale)}`;
  }
  const yes = yesLine.exec(key);
  if (yes) {
    return `${localizeTerm("Yes", locale)} & ${localizeTerm(humanize(yes[2]), locale)} ${yes[3]} ${localizeTerm("Goals", locale)}`;
  }
  const line = overUnderLine.exec(key);
  if (line) {
    return `${localizeTerm(humanize(line[1]), locale)} ${line[2]} ${localizeTerm("Goals", locale)}`;
  }
  // Correct-score style (e.g. "2-1") renders as-is with a dash separator.
  if (/^\d+\s*-\s*\d+$/.test(key)) return key;
  return humanize(key);
}

export function formatMarketOutcome(marketKey: string, selection: string, locale: Locale = "en"): string {
  const key = String(selection ?? "").trim().toLowerCase();
  const line = overUnderLine.exec(key);
  const unit = marketLineUnits[String(marketKey ?? "").trim().toLowerCase()];
  if (line && unit) return `${localizeTerm(humanize(line[1]), locale)} ${line[2]} ${localizeUnit(unit, locale)}`;
  return formatOutcome(key, locale);
}

// Backward-compatible aliases so existing imports keep working while callers
// migrate to the canonical names.
export function marketLabel(marketKey: string, locale: Locale = "en"): string {
  return formatMarket(marketKey, locale);
}

export function selectionLabel(selection: string): string {
  return formatOutcome(selection);
}

export function marketSelectionLabel(marketKey: string, selection: string, locale: Locale = "en"): string {
  return formatMarketOutcome(marketKey, selection, locale);
}

// formatTerm exposes the same localisation table to callers that need a short
// public name rather than a market/selection pair. The ACCA chips use it: the
// BTTS chip read "Yes ACCAs" while BTTS also has a No side, so the selection
// label alone never identified the market.
export function formatTerm(term: string, locale: Locale = "en"): string {
  return localizeTerm(term, locale);
}
