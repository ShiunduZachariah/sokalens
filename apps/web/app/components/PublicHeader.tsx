"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { BetSlip } from "./BetSlip";
import { BrandLogo } from "./BrandLogo";
import { HeaderSearch } from "./HeaderSearch";
import { BUILDER_EVENT_NAME, builderSelectionCount } from "../multi-match-builder/builder-state";
import { ThemeToggle } from "./ThemeToggle";
import { useAccount } from "./account/AccountContext";
import { LanguageSelector } from "./locale/LanguageSelector";
import { useLocale } from "./locale/LocaleProvider";
import { localePath, stripLocalePrefix } from "../i18n/config";
import { ViewerTimezoneLabel } from "./ViewerTimezoneLabel";

const navItems = [
  { label: "nav.home", href: "/" },
  { label: "nav.predictions", href: "/predictions/today" },
  // In-play replaces Search in the header. Search stays reachable at /search
  // and from the search box; a live scoreboard is what a reader arriving
  // mid-evening is actually looking for.
  { label: "nav.in_play", href: "/in-play" },
  { label: "nav.accas", href: "/acca-predictions" },
  { label: "nav.builder", href: "/multi-match-builder" },
] as const;

export function PublicHeader() {
  const pathname = usePathname();
  const activePath = stripLocalePrefix(pathname);
  const { locale, t } = useLocale();
  const { access, status } = useAccount();
  const [builderCount, setBuilderCount] = useState(0);
  const accountLabel = status === "ready" && access?.authenticated ? t("nav.account") : t("nav.sign_in");

  useEffect(() => {
    const update = () => setBuilderCount(builderSelectionCount());
    update();
    const interval = window.setInterval(update, 1_000);
    window.addEventListener(BUILDER_EVENT_NAME, update);
    window.addEventListener("storage", update);
    return () => {
      window.clearInterval(interval);
      window.removeEventListener(BUILDER_EVENT_NAME, update);
      window.removeEventListener("storage", update);
    };
  }, []);

  return (
    <>
      <div className="top-strip">
        <div className="top-strip__inner">
          <span>Statistical estimates, never guaranteed outcomes.</span>
          <span className="top-strip__links">
            <Link href={localePath("/responsible-gambling", locale)} prefetch={false}>Responsible use</Link>
            <ViewerTimezoneLabel />
            <Link href={localePath("/results", locale)} prefetch={false}>How we performed</Link>
            <ThemeToggle />
          </span>
        </div>
      </div>
      <header className="public-header">
        <div className="public-header__inner">
          <BrandLogo />
          <HeaderSearch />
          <div className="header-actions">
            <nav className="header-nav" aria-label="Primary">
              {navItems.map((item) => {
                const isCurrent =
                  activePath === item.href ||
                  (item.href !== "/" && activePath.startsWith(`${item.href}/`));

                const label =
                  item.href === "/multi-match-builder" && builderCount > 0
                    ? t("nav.builder") + " (" + builderCount + ")"
                    : t(item.label);
                return (
                  <Link
                    aria-current={isCurrent ? "page" : undefined}
                    href={localePath(item.href, locale)}
                    key={item.href}
                    prefetch={false}
                  >
                    {label}
                  </Link>
                );
              })}
            </nav>
            {/* Go Pro is hidden while there is nothing to upgrade to. The
                /pro route still exists and is still linked from the footer,
                so nothing is unreachable -- it is off the primary nav only. */}
            {/* Desktop account utility. Mobile uses the fifth bottom-nav slot. */}
            <Link
              aria-current={activePath === "/account" ? "page" : undefined}
              aria-label={accountLabel}
              className="account-link"
              href={localePath(access?.authenticated ? "/account" : "/login", locale)}
              prefetch={false}
              title={accountLabel}
            >
              <span className="account-link__label">{accountLabel}</span>
            </Link>
            <LanguageSelector />
            <BetSlip />
          </div>
        </div>
      </header>
    </>
  );
}
