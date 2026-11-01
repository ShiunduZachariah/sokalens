import { ComingSoon } from "../components/ComingSoon";

export const metadata = {
  title: "Notifications - SokaLens",
  robots: { index: false, follow: false },
};

export default function NotificationsPage() {
  return (
    <ComingSoon
      kicker="Notifications"
      title="Notifications are not available yet"
      message="Alerts and notifications will be available when account features launch."
    />
  );
}
