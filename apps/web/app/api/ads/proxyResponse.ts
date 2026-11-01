export function copyUpstreamCookies(source: Headers, destination: Headers) {
  const cookieHeaders = source as Headers & { getSetCookie?: () => string[] };
  const cookies = cookieHeaders.getSetCookie?.() ?? [];
  if (cookies.length > 0) {
    for (const cookie of cookies) destination.append("Set-Cookie", cookie);
    return;
  }
  const cookie = source.get("set-cookie");
  if (cookie) destination.append("Set-Cookie", cookie);
}
