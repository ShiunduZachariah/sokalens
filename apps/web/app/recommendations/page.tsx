import { ComingSoon } from "../components/ComingSoon";

export const metadata = {
  title: "Recommendations - SokaLens",
  robots: { index: false, follow: false },
};

export default function RecommendationsPage() {
  return (
    <ComingSoon
      kicker="For you"
      title="Personalized recommendations are not available yet"
      message="Recommendation feeds will be available when account features launch."
    />
  );
}
