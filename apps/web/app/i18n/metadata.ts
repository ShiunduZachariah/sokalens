import { localeAlternates, localePath } from "./config";
import { translate, type TranslationKey } from "./catalogs";
import { requestLocaleContext } from "./server";

export async function localizedAuthMetadata(titleKey: TranslationKey, descriptionKey: TranslationKey, basePath: string) {
  const { locale } = await requestLocaleContext();
  const canonical = localePath(basePath, locale);
  const title = translate(locale, titleKey);
  const description = translate(locale, descriptionKey);
  return {
    title: `${title} - SokaLens`, description,
    alternates: { canonical, languages: localeAlternates(basePath) },
    openGraph: { title, description, url: canonical },
    robots: { index: false, follow: false },
  };
}
