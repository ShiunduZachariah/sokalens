import Link from "next/link";
import {
  matchClockLabel,
  matchScore,
  matchStatusLabel,
  predictionStateLabel,
  type LiveMatch,
} from "../data/liveMatches";
import { formatMarket } from "../lib/markets";
import { CompetitionPill } from "./CompetitionPill";
import { ConfidenceMeter } from "./ConfidenceMeter";
import { OddsStrip } from "./OddsStrip";
import { TeamCrest } from "./TeamCrest";
import type { Locale } from "../i18n/config";
import { fixtureHref } from "../lib/fixtureHref";

import { LocalTime } from "./LocalTime";
type MatchCardProps = {
  locale: Locale;
  match: LiveMatch;
  compact?: boolean;
};

const liveStatuses = new Set(["1H", "2H", "HT", "ET", "BT", "P", "LIVE"]);

export function MatchCard({ locale, match, compact = false }: MatchCardProps) {
  const rawStatus = match.rawStatus.trim().toUpperCase();
  const isLive = liveStatuses.has(rawStatus);
  const statusLabel = isLive ? "Live" : matchStatusLabel(match);

  return (
    <article className="match-card">
      <div className="match-card__meta">
        <CompetitionPill name={match.leagueName} />
        <span className={isLive ? "pill pill--coral" : "pill"}>{statusLabel}</span>
      </div>

      <div className="match-card__teams">
        <div className="team-line">
          <TeamCrest logoUrl={match.homeTeamLogo} name={match.homeTeam} size={24} />
          <span>
            <span className="team-name">{match.homeTeam}</span>
          </span>
        </div>
        <div className="score-box">
          <strong>{matchScore(match) ?? <LocalTime timeOnly value={match.kickoffAt} />}</strong>
          <span className="micro">{isLive ? matchClockLabel(match) : "Kickoff"}</span>
        </div>
        <div className="team-line">
          <TeamCrest logoUrl={match.awayTeamLogo} name={match.awayTeam} size={24} />
          <span>
            <span className="team-name">{match.awayTeam}</span>
          </span>
        </div>
      </div>

      <div className="match-card__details">
        {match.prediction ? (
          <ConfidenceMeter
            value={match.prediction.confidence}
            label={`${formatMarket(match.prediction.marketKey, locale)} confidence`}
          />
        ) : (
          <p className="micro">{predictionStateLabel(match.predictionState)}</p>
        )}
      </div>

      {compact ? null : <OddsStrip odds={match.odds} />}

      <div className="match-card__actions">
        <span className="micro">{match.venueName || "Venue to be confirmed"}</span>
        <Link className="button button--secondary" href={fixtureHref(match)}>
          View analysis
        </Link>
      </div>
    </article>
  );
}
