"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { Icon } from "./icons";
import { useAccount } from "./account/AccountContext";
import { useLocale } from "./locale/LocaleProvider";
import { localePath, stripLocalePrefix } from "../i18n/config";

const primaryItems = [
  { label: "nav.home", href: "/", icon: "home" },
  { label: "nav.predictions", href: "/predictions/today", icon: "chart" },
  // Third slot, per the design's six-slot bar. It was missing on every page
  // including /in-play itself, so the live surface had no entry point in mobile
  // primary navigation -- on the breakpoint where live football is watched.
  { label: "nav.in_play", href: "/in-play", icon: "inplay" },
  { label: "nav.accas", href: "/acca-predictions", icon: "trophy" },
  { label: "nav.builder", href: "/multi-match-builder", icon: "builder" },
] as const;

export function MobileBottomNav() {
  const pathname = usePathname();
  const activePath = stripLocalePrefix(pathname);
  const { locale, t } = useLocale();
  const { access, status } = useAccount();
  const authenticated = status === "ready" && Boolean(access?.authenticated);
  const items = [
    ...primaryItems.map((item) => ({ ...item, text: t(item.label) })),
    {
      href: authenticated ? "/account" : "/login",
      icon: "account" as const,
      text: authenticated ? t("nav.account") : t("nav.sign_in"),
    },
  ];

  return (
    <nav className="mobile-bottom-nav" aria-label="Mobile primary">
      <div className="mobile-bottom-nav__inner">
        {items.map((item) => {
          const isCurrent =
            activePath === item.href ||
            (item.href !== "/" && activePath.startsWith(`${item.href}/`));

          return (
            <Link aria-current={isCurrent ? "page" : undefined} href={localePath(item.href, locale)} key={item.href} prefetch={false}>
              <Icon name={item.icon} />
              <span>{item.text}</span>
            </Link>
          );
        })}
      </div>
    </nav>
  );
}
