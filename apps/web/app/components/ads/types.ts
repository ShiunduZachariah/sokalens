import type { ComponentType } from "react";
import type { AdFailureReason, AdProviderAttempt } from "../../lib/adDelivery";

export type AdFormat = "top" | "in-feed" | "rail" | "adhesion" | "banner" | "sidebar" | "inline" | "footer";
export type AdapterResult =
  | { outcome: "success" }
  | { outcome: "failure"; failureReason: AdFailureReason };
export type ProviderAdapterProps = {
  attempt: AdProviderAttempt;
  format: AdFormat;
  onStart?: () => void;
  onResult: (result: AdapterResult) => void;
};
export type ProviderAdapter = ComponentType<ProviderAdapterProps>;
