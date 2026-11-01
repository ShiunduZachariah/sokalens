import { ComingSoon } from "../components/ComingSoon";

export const metadata = {
  title: "Subscription - SokaLens",
  robots: { index: false, follow: false },
};

export default function SubscriptionPage() {
  return (
    <ComingSoon
      kicker="Subscription"
      title="Subscriptions are not available yet"
      message="SokaLens launched as a free, read-only site. Subscription plans will appear in a later release."
    />
  );
}
