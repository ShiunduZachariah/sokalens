import assert from "node:assert/strict";
import test from "node:test";
import { copyUpstreamCookies } from "./proxyResponse.ts";

test("ad proxy preserves every upstream session cookie", () => {
  const source = new Headers();
  source.append("Set-Cookie", "sokalens_ad_session=one; Path=/; HttpOnly");
  source.append("Set-Cookie", "ad_probe=two; Path=/; SameSite=Lax");
  const destination = new Headers();
  copyUpstreamCookies(source, destination);
  const values = destination.getSetCookie?.() ?? [destination.get("set-cookie")];
  assert.match(values.join("\n"), /sokalens_ad_session=one/);
  assert.match(values.join("\n"), /ad_probe=two/);
});
