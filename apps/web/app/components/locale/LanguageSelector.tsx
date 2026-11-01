"use client";

import { localeDetails, supportedLocales, type Locale } from "../../i18n/config";
import { useLocale } from "./LocaleProvider";

export function LanguageSelector() {
  const { locale, setLocale, t } = useLocale();
  return (
    <label className="language-selector">
      {/* .visually-hidden is not defined in globals.css — only .sr-only is —
          so this screen-reader label was rendering as visible "Language" text
          in the header on every page, and crowding the wordmark at mobile
          width. */}
      <span className="sr-only">{t("language.label")}</span>
      <span aria-hidden="true" className="language-selector__icon">文</span>
      <select aria-label={t("language.label")} onChange={(event) => setLocale(event.target.value as Locale)} value={locale}>
        {supportedLocales.map((item) => <option key={item} value={item}>{localeDetails[item].label}</option>)}
      </select>
    </label>
  );
}
