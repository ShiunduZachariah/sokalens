import Link from "next/link";

type ComingSoonProps = {
  kicker: string;
  title: string;
  message?: string;
};

export function ComingSoon({ kicker, title, message }: ComingSoonProps) {
  return (
    <main className="page-shell" id="content">
      <section className="page-intro">
        <div>
          <p className="page-kicker">{kicker}</p>
          <h1 className="page-title">{title}</h1>
          <p className="page-summary">
            {message ?? "This part of SokaLens is not available in the current release."}
          </p>
        </div>
      </section>
      <p>
        <Link className="button button--secondary" href="/predictions/today">
          Explore today&apos;s predictions
        </Link>
      </p>
    </main>
  );
}
