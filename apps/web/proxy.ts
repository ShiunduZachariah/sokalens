import { NextRequest, NextResponse } from "next/server";
import {
  defaultLocale,
  isLocale,
  stripLocalePrefix,
} from "./app/i18n/config";

export function proxy(request: NextRequest) {
  const url = request.nextUrl.clone();
  const pathLocale = url.pathname.split("/")[1];
  const locale = isLocale(pathLocale) ? pathLocale : defaultLocale;

  // Browser, country, and cookie values vary between crawlers and visitors, so
  // they must never change the SSR language or canonical of an unprefixed URL.
  // Explicit preferences are restored client-side onto stable locale URLs.

  const forwardedHeaders = new Headers(request.headers);
  forwardedHeaders.set("x-sokalens-locale", locale);
  forwardedHeaders.set("x-sokalens-public-path", url.pathname);

  // /en/* is the same English tree as the unprefixed root, byte for byte.
  // Serving both split every English page's ranking signals in two, so the
  // prefixed copy is permanently retired. Caddy answers this at the edge in
  // production (infra/deploy/Caddyfile); this branch is the origin-side
  // backstop that also covers local dev and any non-Caddy deployment.
  // 308 rather than 301 so a non-GET request keeps its method and body.
  if (pathLocale === defaultLocale) {
    url.pathname = stripLocalePrefix(url.pathname);
    return NextResponse.redirect(url, 308);
  }

  if (isLocale(pathLocale)) {
    url.pathname = stripLocalePrefix(url.pathname);
    return NextResponse.rewrite(url, { request: { headers: forwardedHeaders } });
  }
  return NextResponse.next({ request: { headers: forwardedHeaders } });
}

export const config = {
  matcher: ["/((?!api|_next/static|_next/image|favicon.ico|robots.txt|sitemap.xml|sitemaps/|.*\\.[a-zA-Z0-9]+$).*)"],
};
