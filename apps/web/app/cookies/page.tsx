import { canonicalPath } from "../site";

export const metadata = {
  title: "Cookies - SokaLens",
  description: "SokaLens cookie and consent overview for the public web.",
  alternates: {
    canonical: canonicalPath("/cookies"),
  },
};

export default function CookiesPage() {
  return (
    <main className="page-shell" id="content">
      <article className="legal-panel">
        <p className="page-kicker">Cookies</p>
        <h1 className="page-title">SokaLens cookie overview</h1>
        <p>Last updated: 2026-08-24.</p>
        <p>
          Public SokaLens football content is available without an account. If you create an
          account, essential cookies keep you signed in and protect account actions. This page
          summarises the storage and tracking technologies the site may use.
        </p>
        <h2>Essential storage</h2>
        <p>
          Essential cookies and similar local storage support core site functions, security, and
          abuse prevention. Account sessions use a secure, HTTP-only authentication cookie, and
          a separate anti-forgery cookie helps protect changes to your profile and favorites.
        </p>
        <p>
          A signed anonymous visitor cookie may support rolling limits on how much detailed
          analysis a visitor can view. It is not an absolute cross-device identity and is not used
          to claim certainty about visitors using private browsing, cleared storage, or VPNs.
        </p>
        <p>
          Local storage may remember interface choices such as theme, odds display, or builder
          selections. These browser-only choices are separate from your account favorites.
        </p>
        <h2>Analytics and ads</h2>
        <p>
          The site may use first-party analytics to understand aggregate usage and account-feature
          adoption, and third-party advertising providers may set their own cookies or similar technologies.
          Their use of these technologies is governed by their own privacy policies, as described
          in our <a href="/privacy">Privacy Policy</a>.
        </p>
        <h2>Changing preferences</h2>
        <p>
          A dedicated non-essential consent centre will be added before analytics or advertising
          scripts are enabled. Public football content remains available without any non-essential
          consent.
        </p>
      </article>
    </main>
  );
}
