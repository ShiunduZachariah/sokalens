import { headers } from "next/headers";
import { defaultLocale, isLocale } from "./config";

export async function requestLocaleContext() {
  const requestHeaders = await headers();
  const candidate = requestHeaders.get("x-sokalens-locale");
  return {
    locale: isLocale(candidate) ? candidate : defaultLocale,
    publicPath: requestHeaders.get("x-sokalens-public-path") ?? "/",
  };
}
