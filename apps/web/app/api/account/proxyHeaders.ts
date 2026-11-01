import { BlockList, isIP } from "node:net";

const cloudflareProxies = new BlockList();
for (const value of [
  "173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22",
  "141.101.64.0/18", "108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20",
  "197.234.240.0/22", "198.41.128.0/17", "162.158.0.0/15", "104.16.0.0/13",
  "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
]) {
  const [network, prefix] = value.split("/");
  cloudflareProxies.addSubnet(network, Number(prefix), "ipv4");
}
for (const value of [
  "2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32",
  "2405:8100::/32", "2a06:98c0::/29", "2c0f:f248::/32",
]) {
  const [network, prefix] = value.split("/");
  cloudflareProxies.addSubnet(network, Number(prefix), "ipv6");
}

function validIP(value: string | null) {
  const candidate = value?.trim() ?? "";
  return isIP(candidate) ? candidate : undefined;
}

function forwardedPeerIP(incoming: Headers) {
  const chain = incoming.get("x-forwarded-for")?.split(",") ?? [];
  return validIP(chain.at(-1) ?? null);
}

function isTrustedCloudflareRequest(incoming: Headers) {
  const peer = forwardedPeerIP(incoming);
  return peer ? cloudflareProxies.check(peer, isIP(peer) === 6 ? "ipv6" : "ipv4") : false;
}

export function sanitizedClientIP(incoming: Headers) {
  if (isTrustedCloudflareRequest(incoming)) {
    const cloudflareIP = validIP(incoming.get("cf-connecting-ip"));
    if (cloudflareIP) return cloudflareIP;
  }
  // Caddy controls the right-most X-Forwarded-For hop. Never trust the first
  // value supplied by a direct origin client.
  return forwardedPeerIP(incoming);
}

export function sanitizedCountry(incoming: Headers) {
  if (!isTrustedCloudflareRequest(incoming)) return undefined;
  const country = incoming.get("cf-ipcountry")?.trim() ?? "";
  return /^[A-Za-z]{2}$/.test(country) ? country.toUpperCase() : undefined;
}

export function accountProxyHeaders(incoming: Headers) {
  const outgoing = new Headers({ Accept: "application/json" });
  for (const name of ["content-type", "cookie", "idempotency-key", "x-csrf-token", "user-agent", "referer"]) {
    const value = incoming.get(name);
    if (value) outgoing.set(name, value);
  }

  const clientIP = sanitizedClientIP(incoming);
  if (clientIP) outgoing.set("X-SokaLens-Client-IP", clientIP);
  const country = sanitizedCountry(incoming);
  if (country) outgoing.set("X-SokaLens-Country", country);
  return outgoing;
}
