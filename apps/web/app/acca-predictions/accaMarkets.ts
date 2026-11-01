export type AccaMarketDefinition = {
  slug: string;
  title: string;
  heading: string;
  description: string;
  marketKeys: string[];
  selection?: string;
  // shortLabel names the market on chips and headings when the selection label
  // would be ambiguous on its own. BTTS Yes rendered as "Yes ACCAs", which said
  // nothing about the market and read as the opposite of a BTTS No slip.
  shortLabel?: string;
};

export const accaMarketDefinitions: AccaMarketDefinition[] = [
  { slug: "1x2-accas", title: "1X2 ACCA Predictions", heading: "1X2 ACCAs", description: "System-generated match-result ACCAs using independently qualified home, draw, and away predictions.", marketKeys: ["1x2"] },
  { slug: "over-2-5-accas", title: "Over 2.5 ACCA Predictions", heading: "Over 2.5 goals ACCAs", description: "System-generated Over 2.5 goals ACCAs from fixtures where that exact total-goals selection clears the engine gates.", marketKeys: ["total_goals"], selection: "over_2.5" },
  { slug: "btts-accas", title: "BTTS ACCA Predictions", heading: "Both teams to score ACCAs", description: "System-generated BTTS Yes ACCAs with one independently qualified selection per fixture.", marketKeys: ["btts"], selection: "yes", shortLabel: "BTTS" },
  { slug: "corners-accas", title: "Corners ACCA Predictions", heading: "Football corners ACCAs", description: "System-generated corners ACCAs shown only where the required match statistics support that market.", marketKeys: ["total_corners"] },
  { slug: "cards-accas", title: "Cards ACCA Predictions", heading: "Football cards ACCAs", description: "System-generated cards ACCAs shown only where the required disciplinary statistics support that market.", marketKeys: ["total_cards"] },
];

export function accaMarketDefinition(slug: string) {
  return accaMarketDefinitions.find((item) => item.slug === slug) ?? null;
}

export function accaMarketDisplayLabel(definition: AccaMarketDefinition, locale: Locale) {
  if (definition.shortLabel) return formatTerm(definition.shortLabel, locale);
  const marketKey = definition.marketKeys[0];
  return definition.selection
    ? marketSelectionLabel(marketKey, definition.selection, locale)
    : marketLabel(marketKey, locale);
}
import type { Locale } from "../i18n/config";
import { formatTerm, marketLabel, marketSelectionLabel } from "../lib/markets";
