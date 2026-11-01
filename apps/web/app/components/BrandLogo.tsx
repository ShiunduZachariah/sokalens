"use client";

import Link from "next/link";
import { localePath } from "../i18n/config";
import { useLocale } from "./locale/LocaleProvider";

type BrandLogoProps = {
  className?: string;
  href?: string;
  tone?: "light" | "dark";
  variant?: "full" | "mark";
};

export function BrandLogo({
  className,
  href = "/",
  tone = "light",
  variant = "full",
}: BrandLogoProps = {}) {
  const { locale } = useLocale();
  const isDark = tone === "dark";
  const classes = [
    "brand-logo",
    `brand-logo--${tone}`,
    variant === "mark" ? "brand-logo--mark-only" : "brand-logo--full",
    className,
  ]
    .filter(Boolean)
    .join(" ");

  return (
    <Link className={classes} href={localePath(href, locale)} prefetch={false} aria-label="SokaLens home">
      <svg
        className="brand-logo__mark"
        viewBox="0 0 40 40"
        aria-hidden="true"
        focusable="false"
      >
        <circle
          cx="17"
          cy="17"
          r="11"
          fill={isDark ? "#162B2B" : "#E6F6EF"}
          stroke={isDark ? "#54D6A3" : "#11845B"}
          strokeWidth="3"
        />
        <path
          d="M25 25L34 34"
          stroke={isDark ? "#FAFCFE" : "#101820"}
          strokeWidth="4"
          strokeLinecap="round"
        />
        <path d="M12 22V17" stroke="#5B8DEF" strokeWidth="3" strokeLinecap="round" />
        <path d="M17 22V12" stroke="#5B8DEF" strokeWidth="3" strokeLinecap="round" />
        <path d="M22 22V15" stroke="#5B8DEF" strokeWidth="3" strokeLinecap="round" />
      </svg>
      {variant === "full" ? (
        <span className="brand-logo__text" style={isDark ? { color: "#FAFCFE" } : undefined}>
          SokaLens
        </span>
      ) : null}
    </Link>
  );
}
