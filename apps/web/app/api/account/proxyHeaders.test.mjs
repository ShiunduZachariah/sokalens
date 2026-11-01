import assert from "node:assert/strict";
import test from "node:test";

import { accountProxyHeaders, sanitizedClientIP, sanitizedCountry } from "./proxyHeaders.ts";

test("Cloudflare client identity is normalized for the trusted upstream header", () => {
  const incoming = new Headers({
    "cf-connecting-ip": " 2001:db8::7 ",
    "cf-ipcountry": "ke",
    "x-forwarded-for": "2001:db8::7, 173.245.48.5",
  });
  assert.equal(sanitizedClientIP(incoming), "2001:db8::7");
  assert.equal(sanitizedCountry(incoming), "KE");
});

test("the Caddy-controlled right-most address is the direct-origin fallback", () => {
  assert.equal(sanitizedClientIP(new Headers({ "cf-connecting-ip": "203.0.113.9", "x-forwarded-for": "198.51.100.4, 192.0.2.8" })), "192.0.2.8");
  assert.equal(sanitizedClientIP(new Headers({ "x-forwarded-for": "invalid, 198.51.100.4" })), "198.51.100.4");
});

test("direct-origin clients cannot forge Cloudflare identity or country", () => {
  const incoming = new Headers({
    "cf-connecting-ip": "203.0.113.9", "cf-ipcountry": "KE",
    "x-forwarded-for": "203.0.113.9, 192.0.2.8",
  });
  assert.equal(sanitizedClientIP(incoming), "192.0.2.8");
  assert.equal(sanitizedCountry(incoming), undefined);
});

test("proxy headers reject spoofed internal identity and forward request context", () => {
  const outgoing = accountProxyHeaders(new Headers({
    "cf-connecting-ip": "203.0.113.9",
    "cf-ipcountry": "KE",
    referer: "https://sokalens.com/register",
    "user-agent": "SokaLens test browser",
    "x-sokalens-client-ip": "192.0.2.99",
    "x-sokalens-country": "US",
    "x-forwarded-for": "203.0.113.9, 173.245.48.5",
  }));
  assert.equal(outgoing.get("x-sokalens-client-ip"), "203.0.113.9");
  assert.equal(outgoing.get("x-sokalens-country"), "KE");
  assert.equal(outgoing.get("user-agent"), "SokaLens test browser");
  assert.equal(outgoing.get("referer"), "https://sokalens.com/register");
});
