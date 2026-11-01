import { AccountDashboard } from "../components/account/AccountDashboard";
import { AccountPageFrame } from "../components/account/AccountPageFrame";

export const metadata = {
  title: "Your account - SokaLens",
  robots: { index: false, follow: false },
};

export default function AccountPage() {
  return (
    <AccountPageFrame
      kicker="Account"
      summary="Manage your profile, active sessions, and privacy from one place."
      title="Your SokaLens settings"
    >
      <AccountDashboard />
    </AccountPageFrame>
  );
}
