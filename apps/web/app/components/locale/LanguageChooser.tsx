"use client";

import { useEffect, useState } from "react";
import { usePathname } from "next/navigation";
import { localeCookieName, localeDetails, localeFromAcceptLanguage, supportedLocales, type Locale } from "../../i18n/config";
import { useLocale } from "./LocaleProvider";

export function LanguageChooser() {
  const { locale, setLocale, t } = useLocale();
  const pathname = usePathname();
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState<Locale>(locale);

  useEffect(() => {
    const hasPreference = document.cookie.split(";").some((part) => part.trim().startsWith(`${localeCookieName}=`));
    const detected = localeFromAcceptLanguage(window.navigator.languages.join(","));
    if (!hasPreference && detected) setSelected(detected);
    const accountEntry = /\/(?:login|register|forgot-password|reset-password|verify-email)\/?$/.test(pathname);
    setOpen(!accountEntry && !hasPreference && window.localStorage.getItem("sokalens.locale.chosen") !== "1");
  }, [pathname]);

  if (!open) return null;
  return (
    <div aria-labelledby="language-chooser-title" aria-modal="true" className="language-chooser" role="dialog">
      <div className="language-chooser__panel">
        <p className="eyebrow">{t("chooser.title")}</p>
        <h2 id="language-chooser-title">{t("chooser.prompt")}</h2>
        <p>{t("chooser.detected", { language: localeDetails[locale].label })}</p>
        <div className="language-chooser__options" role="radiogroup">
          {supportedLocales.map((item) => (
            <label dir={localeDetails[item].direction} key={item} lang={item}>
              <input checked={selected === item} name="preferred-language" onChange={() => setSelected(item)} type="radio" />
              <span>{localeDetails[item].label}</span>
            </label>
          ))}
        </div>
        <button className="button" onClick={() => { setLocale(selected); setOpen(false); }} type="button">{t("chooser.continue")}</button>
      </div>
    </div>
  );
}
