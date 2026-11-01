import type { ReactNode } from "react";
import { AccountNav } from "./AccountNav";

type AccountPageFrameProps = {
  children: ReactNode;
  kicker: string;
  summary: string;
  title: string;
};

export function AccountPageFrame({ children, kicker, summary, title }: AccountPageFrameProps) {
  return (
    <main className="page-shell account-page" id="content">
      <section className="account-hero">
        <div>
          <p className="page-kicker">{kicker}</p>
          <h1 className="page-title">{title}</h1>
          <p className="page-summary">{summary}</p>
        </div>
      </section>
      <AccountNav />
      {children}
    </main>
  );
}
