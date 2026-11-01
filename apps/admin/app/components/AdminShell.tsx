"use client";

import type { ReactNode } from "react";
import { usePathname } from "next/navigation";
import { AdminSidebar } from "./AdminSidebar";
import { AdminTopbar } from "./AdminTopbar";

type AdminShellProps = {
  children: ReactNode;
  /** Hides the Ads entry while ADS_ENABLED is off. */
  adsEnabled?: boolean;
};

const navigation = [
  { label: "Overview", href: "/", short: "OV", section: "Platform" },
  { label: "Matches", href: "/matches", short: "MA", section: "Sports" },
  { label: "Priority", href: "/priority", short: "PR", section: "Sports" },
  { label: "Predictions", href: "/predictions", short: "PE", section: "Predictions" },
  { label: "Providers", href: "/provider", short: "PV", section: "Providers" },
  { label: "Ads", href: "/ads", short: "AD", section: "Commercial" },
  { label: "Users", href: "/users", short: "US", section: "Users" },
  { label: "User Activity", href: "/user-activity", short: "UA", section: "Users" },
  { label: "Subscriptions", href: "/subscriptions", short: "SU", section: "Users" },
  { label: "Notifications", href: "/notifications", short: "NO", section: "Engagement" },
  { label: "Policy", href: "/policy", short: "PO", section: "Policy" },
  { label: "Jobs", href: "/jobs", short: "JB", section: "Operations" },
  { label: "Diagnostics", href: "/diagnostics", short: "DI", section: "Operations" },
  { label: "Audit Logs", href: "/audit-logs", short: "AU", section: "Operations" },
  { label: "Settings", href: "/settings", short: "SE", section: "Operations" },
];

export function AdminShell({ children, adsEnabled = false }: AdminShellProps) {
  const pathname = usePathname();
  const items = adsEnabled ? navigation : navigation.filter((item) => item.href !== "/ads");
  if (pathname === "/login") {
    return <main className="admin-auth-main">{children}</main>;
  }
  return (
    <div className="admin-shell">
      <AdminSidebar navigation={items} />
      <div className="admin-shell__workspace">
        <AdminTopbar />
        <main className="admin-main">{children}</main>
      </div>
    </div>
  );
}
