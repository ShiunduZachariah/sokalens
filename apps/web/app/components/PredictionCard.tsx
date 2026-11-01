import Link from "next/link";
import {
  marketLabel,
  marketSelectionLabel,
  matchLabel,
  type LivePrediction,
} from "../data/livePredictions";

import { ConfidenceMeter } from "./ConfidenceMeter";
import type { Locale } from "../i18n/config";
import { fixtureHref } from "../lib/fixtureHref";
import { LocalTime } from "./LocalTime";

type PredictionCardProps = {
  locale: Locale;
  prediction: LivePrediction;
};

export function PredictionCard({ locale, prediction }: PredictionCardProps) {
  return (
    <article className="prediction-card">
      <div className="prediction-card__meta">
        <span className="pill pill--signal">{marketLabel(prediction.marketKey, locale)}</span>
        <span className="pill">Version {prediction.publishedVersion}</span>
      </div>
      <div>
        <h3>{matchLabel(prediction)}</h3>
        <p className="micro">{prediction.leagueName}</p>
      </div>
      <div className="prediction-card__pick">
        <span className="micro">Model pick</span>
        <strong>{marketSelectionLabel(prediction.marketKey, prediction.selection, locale)}</strong>
      </div>
      <ConfidenceMeter value={prediction.confidence} />
      <div className="prediction-card__actions">
        <span className="micro">{<LocalTime value={prediction.kickoffAt} />}</span>
        <Link className="button button--secondary" href={fixtureHref(prediction)}>
          View analysis
        </Link>
      </div>
    </article>
  );
}
