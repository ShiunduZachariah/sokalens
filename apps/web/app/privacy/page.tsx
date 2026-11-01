import Link from "next/link";
import { canonicalPath } from "../site";

export const metadata = {
  title: "Privacy Policy - SokaLens",
  description:
    "SokaLens privacy policy for public football content, accounts, and saved football features.",
  alternates: {
    canonical: canonicalPath("/privacy"),
  },
};

export default function PrivacyPage() {
  return (
    <main className="page-shell" id="content">
      <article className="legal-panel">
        <p className="page-kicker">Privacy</p>
        <h1 className="page-title">SokaLens privacy policy</h1>
        <p>
          This Privacy Policy explains how SokaLens ("we", "our", or "us") handles
          information when you use our football prediction and match-discovery website,
          including when you create and use an account.
        </p>

        <h2>1. Overview</h2>
        <p>
          Public football content does not require an account. If you choose to create
          one, we collect the information needed to secure the account, verify your
          email, remember your favorites, and provide the settings you request.
        </p>

        <h2>2. Information we collect</h2>
        <p>When you visit SokaLens, we may collect the following categories of information:</p>
        <ul>
          <li>
            <strong>Anonymous usage data</strong> — page views, sessions identified by
            an anonymous identifier, device and browser type, coarse country, referral
            source, and search terms.
          </li>
          <li>
            <strong>Account information</strong> — your email address, display name,
            country code, password hash, verification status, preferences, consent
            choices, and account timestamps.
          </li>
          <li>
            <strong>Security and saved-feature information</strong> — session and device
            details, security and abuse-prevention signals, and the teams, leagues, and
            matches you save as favorites.
          </li>
        </ul>
        <p>
          We do not collect account identity from people who only browse public pages
          and do not register. We never store your password in readable form.
        </p>

        <h2>3. How we use information</h2>
        <p>We use the information we collect to:</p>
        <ul>
          <li>improve our content and the relevance of what we show;</li>
          <li>measure traffic and understand how the site is used;</li>
          <li>create, secure, verify, and administer accounts;</li>
          <li>store and display your favorites across devices;</li>
          <li>prevent automated abuse, fraud, and unauthorized access; and</li>
          <li>diagnose and fix technical issues.</li>
        </ul>

        <h2>4. Analytics</h2>
        <p>
          We use first-party anonymous analytics to understand aggregate usage of the
          site. We may also measure aggregate registration, verification, login, and
          favorite-use events so we can understand whether account features work. We
          do not use these measurements to create advertising profiles from account
          identity.
        </p>

        <h2>5. Advertising</h2>
        <p>
          SokaLens does not show advertising. We do not work with advertising
          providers, and we do not share your information with anyone for ad
          targeting.
        </p>

        <h2>6. Cookies</h2>
        <p>
          We may set essential cookies and analytics cookies. Essential cookies are
          needed for the site to work; analytics cookies are optional. For a summary of the cookies
          we use, see our <Link href="/cookies">Cookie Policy</Link>.
        </p>

        <h2>7. Third-party links</h2>
        <p>
          SokaLens may link to third-party websites, including clubs, leagues, and
          data providers. We are not responsible for the privacy practices of those
          websites, and their own policies apply when you visit them.
        </p>

        <h2>8. Data retention</h2>
        <p>
          Anonymous and aggregated information may be retained for operational and
          analytics purposes. Account and session information is retained while your
          account is active and for limited periods afterward where needed for security,
          legal, fraud-prevention, or deletion-audit obligations. You can request
          deletion from the account page; deletion does not require removing genuinely
          anonymous aggregate statistics.
        </p>

        <h2>9. Children</h2>
        <p>
          SokaLens is not directed at children under 16, and people under 16 must not
          create an account. We do not knowingly collect account information from
          children.
        </p>

        <h2>10. Changes to this policy</h2>
        <p>
          We may update this Privacy Policy from time to time. Changes will be posted
          on this page with an updated date.
        </p>
        <p>Last updated: 2026-11-01.</p>

        <h2>11. Contact</h2>
        <p>
          If you have questions about this Privacy Policy, contact us at{" "}
          <a href="mailto:privacy@sokalens.com">privacy@sokalens.com</a>.
        </p>
      </article>
    </main>
  );
}
