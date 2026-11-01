import type { ReactNode } from "react";
import { AdminShell } from "./components/AdminShell";
import { adsEnabled } from "./data/adsEnabled";
import "./globals.css";

export const metadata = {
  title: "SokaLens Admin",
  description: "SokaLens operational dashboard for ingestion, predictions, policy, and audit controls.",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: ReactNode;
}>) {
  return (
    <html lang="en">
      <body>
        <AdminShell adsEnabled={adsEnabled()}>{children}</AdminShell>
      </body>
    </html>
  );
}
