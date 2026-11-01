import { ComingSoon } from "../components/ComingSoon";

export const metadata = {
  title: "Tune your feed - SokaLens",
  robots: { index: false, follow: false },
};

export default function OnboardingPage() {
  return (
    <ComingSoon
      kicker="Onboarding"
      title="Personalized feeds are not available yet"
      message="Interest and preference tuning will be available when account features launch."
    />
  );
}
