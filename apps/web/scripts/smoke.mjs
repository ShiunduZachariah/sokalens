import { existsSync, readdirSync, readFileSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = dirname(dirname(fileURLToPath(import.meta.url)));

const requiredFiles = [
  "app/page.tsx",
  "app/football/page.tsx",
  "app/football/today/page.tsx",
  "app/matches/today/page.tsx",
  "app/matches/[id]/page.tsx",
  "app/predictions/today/page.tsx",
  "app/best-bets/page.tsx",
  "app/leagues/page.tsx",
  "app/results/page.tsx",
  "app/leagues/[slug]/page.tsx",
  "app/teams/[slug]/page.tsx",
  "app/multi-match-builder/page.tsx",
  "app/acca-predictions/page.tsx",
  "app/search/page.tsx",
  "app/pro/page.tsx",
  "app/responsible-gambling/page.tsx",
  "app/terms/page.tsx",
  "app/privacy/page.tsx",
  "app/cookies/page.tsx",
  "app/data/catalog.ts",
  "app/data/liveMatches.ts",
  "app/data/livePredictions.ts",
  "app/components/BrandLogo.tsx",
  "app/components/PublicHeader.tsx",
  "app/components/MobileBottomNav.tsx",
  "app/components/SearchBox.tsx",
  "app/components/DateTabs.tsx",
  "app/components/MatchCard.tsx",
  "app/components/PredictionCard.tsx",
  "app/components/ConfidenceMeter.tsx",
  "app/components/OddsStrip.tsx",
  "app/components/FilterBar.tsx",
  "app/components/ResponsibleNote.tsx",
  "app/components/EmptyState.tsx",
  "app/components/ErrorState.tsx",
];

const bannedPhrases = [
  ["sure", " win"],
  ["guaran", "teed acca"],
  ["guaran", "teed win"],
  ["risk", "-free"],
  ["fixed", " match"],
  ["inside", " info"],
  ["stake", " now"],
  ["bet", " now"],
  ["jack", "pot"],
  ["cash", "out"],
  ["pay", "out"],
].map((parts) => parts.join(""));

const failures = [];

for (const file of requiredFiles) {
  if (!existsSync(join(root, file))) {
    failures.push(`Missing required file: ${file}`);
  }
}

const sourceFiles = [];

function walk(directory) {
  for (const entry of readdirSync(directory)) {
    const absolutePath = join(directory, entry);
    const stats = statSync(absolutePath);

    if (stats.isDirectory()) {
      walk(absolutePath);
      continue;
    }

    if (/\.(css|ts|tsx)$/.test(entry)) {
      sourceFiles.push(absolutePath);
    }
  }
}

walk(join(root, "app"));

const searchableText = sourceFiles
  .map((file) => {
    const label = relative(root, file);
    return `\n${label}\n${readFileSync(file, "utf8")}`;
  })
  .join("\n")
  .toLowerCase();

for (const phrase of bannedPhrases) {
  if (searchableText.includes(phrase)) {
    failures.push(`Banned outcome/sportsbook phrase found: "${phrase}"`);
  }
}

if (searchableText.includes("api-football")) {
  failures.push("Frontend source must not call or reference API-Football directly.");
}

if (failures.length > 0) {
  console.error("SokaLens web smoke check failed:");
  for (const failure of failures) {
    console.error(`- ${failure}`);
  }
  process.exit(1);
}

console.log("SokaLens web smoke check passed.");
