import { OddsPreferenceForm } from "../components/account/OddsPreferenceForm";
import { canonicalPath } from "../site";

export const metadata = {
  title: "Choose your odds format - SokaLens",
  alternates: { canonical: canonicalPath("/odds-preference") },
  robots: { index: false, follow: false },
};

export default async function OddsPreferencePage({ searchParams }: { searchParams: Promise<{ next?: string }> }) {
  const { next } = await searchParams;
  return <OddsPreferenceForm nextPath={next} />;
}
