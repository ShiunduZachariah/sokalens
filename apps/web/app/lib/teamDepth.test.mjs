import assert from "node:assert/strict";
import test from "node:test";

import { isIndexableTeam, teamForm } from "./teamDepth.ts";

const team = {
  name: "Gor Mahia",
  recent_matches: [
    { id: 1, league_name: "L", home_team: "Gor Mahia", away_team: "AFC", kickoff_at: "", status: "finished", home_score: 2, away_score: 0 },
    { id: 2, league_name: "L", home_team: "Tusker", away_team: "Gor Mahia", kickoff_at: "", status: "finished", home_score: 1, away_score: 1 },
    { id: 3, league_name: "L", home_team: "Gor Mahia", away_team: "Bandari", kickoff_at: "", status: "finished", home_score: 0, away_score: 3 },
    // Unplayed: must not reach any denominator, or every rate on the page is
    // quietly wrong.
    { id: 4, league_name: "L", home_team: "Gor Mahia", away_team: "Ulinzi", kickoff_at: "", status: "scheduled" },
  ],
};

test("form counts only settled matches, from the club's own perspective", () => {
  const form = teamForm(team);

  assert.equal(form.matches, 3);
  assert.deepEqual([form.won, form.drawn, form.lost], [1, 1, 1]);
  // Scored 2 + 1 + 0, conceded 0 + 1 + 3 -- read per side, not per column.
  assert.equal(form.goalsFor, 3);
  assert.equal(form.goalsAgainst, 4);
  assert.equal(form.cleanSheets, 1);
  assert.equal(form.bothTeamsScored, 1);
  // 2-0 and 1-1 are two goals apiece; only 0-3 clears 2.5.
  assert.equal(form.overTwoFive, 1);
});

test("a club with no played matches is not indexable", () => {
  // 414 team pages produced 3 clicks from 2,206 impressions. A page that can
  // compute nothing about its own subject stays out of the index -- and out of
  // teams.xml, which is built from the same signal.
  assert.equal(isIndexableTeam(teamForm({ name: "New FC", recent_matches: [] })), false);
  assert.equal(isIndexableTeam(teamForm({
    name: "New FC",
    recent_matches: [{ id: 9, league_name: "L", home_team: "New FC", away_team: "X", kickoff_at: "", status: "scheduled" }],
  })), false);
  assert.equal(isIndexableTeam(teamForm(team)), true);
});
