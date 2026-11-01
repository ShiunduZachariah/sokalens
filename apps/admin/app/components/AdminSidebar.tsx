"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";

type NavigationItem = {
  label: string;
  href: string;
  short: string;
  section: string;
};

type AdminSidebarProps = {
  navigation: NavigationItem[];
};

export function AdminSidebar({ navigation }: AdminSidebarProps) {
  const pathname = usePathname();
  const grouped = navigation.reduce<Record<string, NavigationItem[]>>((sections, item) => {
    sections[item.section] = [...(sections[item.section] ?? []), item];
    return sections;
  }, {});

  return (
    <aside className="admin-sidebar">
      <Link className="brand-lockup" href="/" aria-label="SokaLens admin overview">
        <span className="brand-mark" aria-hidden="true">
          <span />
        </span>
        <span>
          <strong>SokaLens</strong>
          <small>Admin</small>
        </span>
      </Link>
      <span className="environment-badge">Production</span>
      <nav aria-label="Admin navigation">
        {Object.entries(grouped).map(([section, items]) => (
          <div className="nav-group" key={section}>
            <p>{section}</p>
            {items.map((item) => {
              const isActive = item.href === "/" ? pathname === "/" : pathname.startsWith(item.href);

              return (
                <Link
                  aria-current={isActive ? "page" : undefined}
                  className={isActive ? "nav-link is-active" : "nav-link"}
                  href={item.href}
                  key={item.href}
                >
                  <span className="nav-link__icon" aria-hidden="true">
                    {item.short}
                  </span>
                  <span className="nav-link__label">{item.label}</span>
                </Link>
              );
            })}
          </div>
        ))}
      </nav>
    </aside>
  );
}
