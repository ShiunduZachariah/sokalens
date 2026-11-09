"use client";

import Link from "next/link";
import { ShareButton } from "../../components/ShareButton";
import { FavoriteButton } from "../../components/account/FavoriteButton";
import { AddToSlipButton } from "../../components/match/AddToSlipButton";
import { ClockIcon } from "../../components/match/icons";
import { TrackerChip } from "../../components/TrackerChip";
import type { LiveMatchPrediction } from "../../data/liveMatches";
import { marketLabel, marketSelectionLabel } from "../../data/livePredictions";
import { matchPredictionSharePath, marketSeoPath, seoSlug } from "../../lib/seo";
import { predictionLabel } from "../../i18n/catalogs";
import { localePath } from "../../i18n/config";
import { useLocale } from "../../components/locale/LocaleProvider";

type MatchPredictionCardProps = {
  kickoffAt: string;
  matchId: number;
  matchStatus: string;
  matchTitle: string;
  /**
   * Forwarded straight onto the Builder leg, so a pick added from a market card
   * reaches the ticket with its crests. The card draws no team block of its own
   * -- the page header above it already carries the pair.
   */
  homeTeam?: string;
  awayTeam?: string;
  homeTeamLogo?: string;
  awayTeamLogo?: string;
  prediction: LiveMatchPrediction;
  /** Rings the card that matches the page's headline pick. */
  top?: boolean;
  /** One-line rationale from the preview builder, when it has one. */
  reason?: string;
};

/** Whether the market has a grade, as opposed to a live tracker reading. */
function isSettled(prediction: LiveMatchPrediction) {
  return prediction.outcome === "won" || prediction.outcome === "lost" || prediction.outcome === "void";
}

function outcomeLabel(prediction: LiveMatchPrediction) {
  if (prediction.outcome === "won") return "Won";
  if (prediction.outcome === "lost") return "Lost";
  if (prediction.outcome === "void") return "Void";
  return "Pending";
}

/**
 * Says why a prediction was voided, when the reason is not a market rule.
 *
 * A bare "Void" on twenty cards is barely better than the "Pending" it
 * replaces: both leave the reader guessing whether the site is broken. These
 * two voids are the ones that need saying out loud, because in each case the
 * honest answer is that no result exists to grade against -- not that the
 * prediction lost.
 */
function voidExplanation(prediction: LiveMatchPrediction) {
  if (prediction.outcome !== "void") return "";
  if (prediction.voidReason === "result_never_arrived") {
    return "No confirmed final result for this match, so the prediction was voided rather than graded against an unfinished score.";
  }
  if (prediction.voidReason === "fixture_not_played") {
    return "This match was not played to a result, so the prediction was voided.";
  }
  if (prediction.voidReason === "settlement_unavailable") {
    // Deliberately does not claim the result is missing -- it exists. Saying
    // "no result" here would be a lie the reader could check against the
    // score shown further up the same page.
    return "This prediction could not be graded against the final result, so it was voided.";
  }
  return "";
}

export function MatchPredictionCard(props: MatchPredictionCardProps) {
  const { locale } = useLocale();
  const { awayTeam, awayTeamLogo, homeTeam, homeTeamLogo, kickoffAt, matchId, matchStatus, matchTitle, prediction, reason, top = false } = props;

  const marketPath = marketSeoPath(prediction.marketKey, prediction.selection);
  const readableMarket = marketLabel(prediction.marketKey, locale);
  const readableSelection = predictionLabel(locale, prediction.marketKey, prediction.selection);
  const predictionAnchor = `prediction-${seoSlug(readableMarket)}-${seoSlug(readableSelection)}`;
  const voidNote = voidExplanation(prediction);
  const actual = prediction.actual
    ? marketSelectionLabel(prediction.marketKey, prediction.actual, locale)
    : "";
  const sharePath = localePath(
    matchPredictionSharePath(matchId, readableMarket, readableSelection),
    locale,
  );
  const confidence = Math.round(prediction.confidence);
  const settled = isSettled(prediction);
  const providerFallback = prediction.source === "provider";

  return (
    <article
      className={top ? "mp-market mp-market--top" : "mp-market"}
      id={predictionAnchor}
    >
      <div className="mp-market__head">
        <span className="mp-market__name">
          {marketPath ? <Link href={localePath(marketPath, locale)}>{readableMarket}</Link> : readableMarket}
        </span>
        {/*
          Settlement wins over the tracker, and the tracker replaces "Pending".

          The two are independent records -- a pick reads `won` on the tracker
          while `outcome` is still `pending`, because settlement writes a
          different row -- so both can be present at once. Rendering both gives
          "Pending · Won" on the same card, which reads as a contradiction. Once
          the market is settled that grade is the answer and the live tracker is
          history; before then the tracker is strictly more informative than
          "Pending", and the contract guarantees the two cannot disagree, because
          the tracker grades through the same code settlement does.
        */}
        {providerFallback ? (
          <span className="mp-market__state">Provider pre-match</span>
        ) : settled ? (
          <span className={`mp-market__state mp-market__state--${prediction.outcome}`}>
            <ClockIcon />
            {outcomeLabel(prediction)}
          </span>
        ) : prediction.trackerState ? (
          <TrackerChip state={prediction.trackerState} />
        ) : (
          <span className={`mp-market__state mp-market__state--${prediction.outcome}`}>
            <ClockIcon />
            {outcomeLabel(prediction)}
          </span>
        )}
      </div>

      <h3 className="mp-market__pick">{readableSelection}</h3>

      <div className="mp-market__figure">
        <span className="mp-market__pct">{confidence}%</span>
        {/*
          The bar is the percentage restated, so it is decoration to a screen
          reader -- the figure beside it already carries the value.
        */}
        <span aria-hidden="true" className="mp-market__track">
          <i className="mp-bar__fill" style={{ width: `${confidence}%` }} />
        </span>
      </div>

      {providerFallback
        ? <p className="mp-market__reason">Provider estimate; SokaLens has no engine prediction for this fixture.</p>
        : reason ? <p className="mp-market__reason">{reason}</p> : <p className="mp-market__reason" />}

      {actual ? <p className="mp-market__reason">Result: {actual}</p> : null}
      {voidNote ? (
        <p className="mp-market__reason" role="note">
          {voidNote}
        </p>
      ) : null}

      <div className="mp-market__actions">
        {!providerFallback ? <AddToSlipButton
          className="mp-btn"
          target={{
            probability: prediction.probability,
            kickoffAt,
            marketKey: prediction.marketKey,
            matchId,
            matchStatus,
            matchTitle,
            homeTeam,
            awayTeam,
            homeTeamLogo,
            awayTeamLogo,
            predictionId: prediction.id,
            selection: prediction.selection,
          }}
        /> : null}
        {!providerFallback ? <FavoriteButton
          compact
          entityId={String(prediction.id)}
          entityType="prediction"
          hideForGuests
          label={`${matchTitle}: ${readableMarket} — ${readableSelection}`}
          returnPath={sharePath}
        /> : null}
        <ShareButton
          compact
          entityId={String(prediction.id)}
          entityType="prediction"
          path={sharePath}
          text={`${matchTitle}: ${readableMarket} — ${readableSelection} on SokaLens`}
          title={`${readableMarket} prediction`}
        />
      </div>
    </article>
  );
}
