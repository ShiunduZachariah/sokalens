import { canonicalPath } from "../site";

export const metadata = {
  title: "Terms of Use - SokaLens",
  description:
    "SokaLens terms of use for football predictions, accounts, and saved football features.",
  alternates: {
    canonical: canonicalPath("/terms"),
  },
};

export default function TermsPage() {
  return (
    <main className="page-shell" id="content">
      <article className="legal-panel">
        <p className="page-kicker">Terms</p>
        <h1 className="page-title">SokaLens terms of use</h1>
        <p>Last updated: 2026-11-01.</p>
        <p>
          These Terms of Use govern your access to and use of the SokaLens website
          ("SokaLens", "we", "our", or "us"). By accessing or using SokaLens, you
          agree to these Terms. If you do not agree to them, please do not use the
          site.
        </p>

        <h2>1. Acceptance of terms</h2>
        <p>
          By visiting or using SokaLens, you confirm that you accept these Terms and
          the policies referenced in them, including our Privacy Policy and Cookie
          Policy. You may not use the site if you do not agree to these Terms, and
          you must not use the site where doing so would be unlawful.
        </p>

        <h2>2. Services provided</h2>
        <p>
          SokaLens is a football prediction and match-discovery website. It displays
          football fixtures, predictions (statistical estimates), odds context, and
          league, team, and match pages for informational purposes.
        </p>
        <p>
          Public football content remains available without an account. You may create
          a free account to save teams, leagues, and matches across devices and to
          manage your profile, verification status, and active sessions. SokaLens does
          not currently accept public posts, uploads, or other user-generated content.
        </p>
        <p>
          You are responsible for providing accurate registration information, keeping
          your password confidential, and promptly revoking sessions you do not
          recognise. You must control the email address used for your account and
          complete email verification using the one-time link we send. We may restrict
          or suspend accounts used for abuse, automation, fraud, or unlawful activity.
        </p>

        <h2>3. No betting or gambling services</h2>
        <p>
          SokaLens does not offer betting, gambling, staking, wallets, or bet
          placement of any kind, and it does not accept or hold funds. Predictions are
          statistical estimates provided for entertainment and information only. They
          are not guaranteed outcomes, tips, or advice, and nothing on the site should
          be treated as an instruction or encouragement to gamble.
        </p>

        <h2>4. Acceptable use</h2>
        <p>When using SokaLens, you agree not to:</p>
        <ul>
          <li>
            scrape, crawl, or extract content abusively, or at a rate that
            unreasonably loads or disrupts our servers;
          </li>
          <li>
            attempt to overload, disrupt, compromise, or gain unauthorized access to
            the site or its infrastructure;
          </li>
          <li>
            misrepresent SokaLens content, including presenting statistical
            predictions as certainties;
          </li>
          <li>use the site for any unlawful, fraudulent, or harmful purpose; or</li>
          <li>
            interfere with the operation of the site or with other visitors' use of
            it.
          </li>
        </ul>

        <h2>5. Intellectual property</h2>
        <p>
          The SokaLens content, design, software, and brand are owned by or licensed
          to SokaLens and are protected by applicable intellectual property laws. You
          may access the site for personal, non-commercial use, but you may not
          reproduce, distribute, or commercially exploit its content without our
          permission.
        </p>
        <p>
          Trademarks, names, and logos of third parties (including football clubs,
          leagues, competitions, and data providers) remain the property of their
          respective owners and are used on the site for identification purposes only.
        </p>

        <h2>6. Disclaimers</h2>
        <p>
          The site and its content are provided "as is" and "as available", without
          warranties of any kind, whether express or implied. We do not warrant that
          the content is accurate, complete, current, or error-free; predictions and
          data may be delayed, inaccurate, or subject to change.
        </p>
        <p>
          To the maximum extent permitted by law, SokaLens is not liable for any
          decisions made, or actions taken, in reliance on the site's content,
          including any loss or damage arising from your use of the site.
        </p>

        <h2>7. Analytics</h2>
        <p>
          The site does not display advertising. It uses privacy-conscious first-party
          analytics, including aggregate account-registration and feature-use
          measurements, as described in our Privacy Policy and Cookie Policy.
        </p>

        <h2>8. Termination and changes</h2>
        <p>
          SokaLens may modify, suspend, or discontinue the site, or any part of it, at
          any time with or without notice. We may also update these Terms from time to
          time. Your continued use of the site after changes are posted constitutes
          acceptance of the then-current Terms.
        </p>

        <h2>9. Governing law</h2>
        <p>
          The site is provided from Kenya, and these Terms are governed by the laws of
          Kenya. Any dispute arising out of or relating to these Terms or the site
          will be subject to the jurisdiction of the courts of Kenya. Nothing in these
          Terms limits any additional rights you may have under applicable consumer
          law.
        </p>

        <h2>10. Contact</h2>
        <p>
          If you have questions about these Terms, contact us at{" "}
          <a href="mailto:support@sokalens.com">support@sokalens.com</a>.
        </p>
      </article>
    </main>
  );
}
