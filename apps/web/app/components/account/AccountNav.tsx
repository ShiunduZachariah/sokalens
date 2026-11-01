"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

const items = [
  { href: "/account", label: "Account" },
  { href: "/favorites", label: "Favorites" },
];

export function AccountNav() {
  const pathname = usePathname();
  return (
    <nav className="account-nav" aria-label="Account">
      {items.map((item) => (
        <Link
          aria-current={pathname === item.href ? "page" : undefined}
          href={item.href}
          key={item.href}
        >
          {item.label}
        </Link>
      ))}
    </nav>
  );
}
