"use client";

import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import { usePathname, useRouter } from "next/navigation";
import { accountApiRequest } from "../../data/accountApi";
import { defaultLocale, isLocale, localeCookieName, localeDetails, localeFromPath, localePath, stripLocalePrefix, type Locale } from "../../i18n/config";
import { translate, type TranslationKey } from "../../i18n/catalogs";
import { useAccount } from "../account/AccountContext";

const CHOSEN_KEY = "sokalens.locale.chosen";
const PENDING_SYNC_KEY = "sokalens.locale.pending-account-sync";
const ACCOUNT_LANGUAGE_KEY = "sokalens.locale.account-language";

type LocaleContextValue = {
  locale: Locale;
  setLocale: (locale: Locale) => void;
  t: (key: TranslationKey, values?: Record<string, string | number>) => string;
};

const LocaleContext = createContext<LocaleContextValue | null>(null);

function persist(locale: Locale) {
  document.cookie = `${localeCookieName}=${locale}; Path=/; Max-Age=31536000; SameSite=Lax`;
  window.localStorage.setItem(CHOSEN_KEY, "1");
  document.documentElement.lang = locale;
  document.documentElement.dir = localeDetails[locale].direction;
}

export function LocaleProvider({ children, initialLocale = defaultLocale }: { children: ReactNode; initialLocale?: Locale }) {
  const { access, profile, profileStatus } = useAccount();
  const pathname = usePathname();
  const router = useRouter();
  const urlLocale = localeFromPath(pathname);
  const [locale, setLocaleState] = useState<Locale>(urlLocale ?? initialLocale);

  const navigate = useCallback((nextLocale: Locale, method: "push" | "replace" = "push") => {
    const query = typeof window === "undefined" ? "" : window.location.search.slice(1);
    const path = `${stripLocalePrefix(pathname)}${query ? `?${query}` : ""}`;
    router[method](localePath(path, nextLocale));
  }, [pathname, router]);

  const saveAccountLanguage = useCallback(async (nextLocale: Locale) => {
    if (!access?.authenticated) return;
    const result = await accountApiRequest("/v1/me/profile", {
      body: JSON.stringify({ preferred_language: nextLocale }),
      headers: access.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "PATCH",
    });
    if (result.ok) {
      window.localStorage.setItem(ACCOUNT_LANGUAGE_KEY, nextLocale);
      window.localStorage.removeItem(PENDING_SYNC_KEY);
    }
  }, [access?.authenticated, access?.csrfToken]);

  const setLocale = useCallback((nextLocale: Locale) => {
    setLocaleState(nextLocale);
    persist(nextLocale);
    if (access?.authenticated) void saveAccountLanguage(nextLocale);
    else window.localStorage.setItem(PENDING_SYNC_KEY, nextLocale);
    navigate(nextLocale);
  }, [access?.authenticated, navigate, saveAccountLanguage]);

  useEffect(() => {
    if (!urlLocale || urlLocale === locale) return;
    setLocaleState(urlLocale);
    document.documentElement.lang = urlLocale;
    document.documentElement.dir = localeDetails[urlLocale].direction;
  }, [locale, urlLocale]);

  useEffect(() => {
    if (urlLocale) return;
    const stored = document.cookie
      .split(";")
      .map((part) => part.trim().split("="))
      .find(([name]) => name === localeCookieName)?.[1];
    if (isLocale(stored)) navigate(stored, "replace");
  }, [navigate, urlLocale]);

  useEffect(() => {
    if (urlLocale || profileStatus !== "ready" || !profile || !isLocale(profile.language)) return;
    const pending = window.localStorage.getItem(PENDING_SYNC_KEY);
    const accountWasSelected = window.localStorage.getItem(ACCOUNT_LANGUAGE_KEY);
    const pendingLocale = isLocale(pending) ? pending : undefined;
    const shouldMigrateGuest = pendingLocale && !accountWasSelected && profile.language === defaultLocale;
    const resolved = shouldMigrateGuest ? pendingLocale : profile.language;
    setLocaleState(resolved);
    persist(resolved);
    if (shouldMigrateGuest) void saveAccountLanguage(resolved);
    else window.localStorage.setItem(ACCOUNT_LANGUAGE_KEY, resolved);
    navigate(resolved, "replace");
  }, [navigate, profile, profileStatus, saveAccountLanguage, urlLocale]);

  const value = useMemo<LocaleContextValue>(() => ({
    locale,
    setLocale,
    t: (key, values) => translate(locale, key, values),
  }), [locale, setLocale]);
  return <LocaleContext.Provider value={value}>{children}</LocaleContext.Provider>;
}

export function useLocale() {
  const value = useContext(LocaleContext);
  if (!value) throw new Error("useLocale must be used within LocaleProvider");
  return value;
}
