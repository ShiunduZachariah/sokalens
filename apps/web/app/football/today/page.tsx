import type { Metadata } from "next";
import { permanentRedirect } from "next/navigation";
import { canonicalPath } from "../../site";

export const metadata: Metadata = {
  title: "Today's football matches - SokaLens",
  description: "Today's football fixtures grouped by competition with prediction availability.",
  alternates: { canonical: canonicalPath("/matches/today") },
  robots: { index: false, follow: true },
};

export default function LegacyTodayPage() {
  permanentRedirect("/matches/today");
}
