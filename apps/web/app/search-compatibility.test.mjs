import test from "node:test";
import assert from "node:assert/strict";
import fs from "node:fs";

const read = (path) => fs.readFileSync(new URL(path, import.meta.url), "utf8");

// The results page has room for a prediction and an ACCA and a user who has
// stopped typing, so it opts in.
test("the search results page opts into prediction and ACCA recommendations", () => {
  assert.match(read("search/page.tsx"), /include_recommendations=true/);
});

// The autocomplete does not. Enriching a four-row dropdown cost +250-350ms on
// production — measured as 300-450ms with the flag against 65-95ms without it
// — on the one request that has to keep up with a keystroke. If this ever goes
// back, the dropdown gets slow again and nothing else will say why.
test("the suggest proxy stays off the recommendation joins", () => {
  const source = read("api/search/suggest/route.ts");
  assert.doesNotMatch(source, /include_recommendations=/);
});

// The API must keep honouring the flag for the page that still sends it.
test("the API contract for the flag is unchanged", () => {
  assert.match(
    read("../../../services/api/internal/httpserver/server.go"),
    /include_recommendations/,
  );
});
