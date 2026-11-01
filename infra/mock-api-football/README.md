# mock-api-football

A static nginx stand-in for API-Football. QA runs against it
(`ENABLE_REAL_API_FOOTBALL_CALLS=false`, `ENABLE_MOCK_API_FOOTBALL_CALLS=true`)
so that testing costs no provider quota — QA and production share one daily
allowance, and the quota planner does not know a second consumer exists.

## Routes

| Route | Serves |
|---|---|
| `/countries` | `fixtures/countries.json` |
| `/fixtures?live=all` | `fixtures/fixtures-live.json` |
| anything else | `404 {"errors":{"route":"not found"},"response":[]}` |

`/fixtures` without `live=all` deliberately still 404s. The same path is polled
by date, by ids and by league, and answering those with the live payload would
write the same three fixtures onto every date QA ingests.

## Testing in-play in QA

`fixtures-live.json` is what makes the in-play feature testable without a real
live match. The event timeline is built from the event list **embedded in this
response** — that is why it costs no extra provider request — so a mock that
omits the events cannot exercise the writer at all.

The file is the real captured payload from
`packages/contracts/provider/api-football/fixtures-live-events.json` (two
fixtures, 2026-09-15, 20 events) plus a third, synthetic fixture whose ids are
outside the provider's range. That fixture exists because the captured sample
happens to contain no awkward cases, and the awkward cases are the whole
difficulty of this feature:

| Event | Trap it exercises |
|---|---|
| `Goal` / `Own Goal` | `detail` carries the meaning, `type` does not — both are `type=Goal` |
| `Goal` / `Penalty`, `Goal` / `Missed Penalty` | a miss contains "penalty" and must not classify as a goal |
| `subst` / `Substitution 1` | the one lower-case `type`; a case-sensitive parser drops every substitution |
| `Card` / `Second Yellow card` | contains "yellow", so it must be checked before the plain yellow |
| `Var` / `Goal cancelled` at 71' withdrawing the 70' goal | retraction is a flag, not a delete |
| `Goal` at 90+3 | added time is a separate field; one column collapses it onto 90' |

To run the in-play path end to end in QA:

```bash
# 1. turn the writer on (it ships off)
#    ENABLE_FIXTURE_EVENTS=true in /home/dev/sokalens/.env.qa
# 2. deploy, which also restarts the worker onto the flag
sudo -u dev env HOME=/home/dev bash /home/dev/sokalens/scripts/qa/deploy.sh
# 3. the live poll is scheduled only inside a kickoff watch window, so for an
#    on-demand run, enqueue one:
#      insert into jobs (job_key, queue, priority, payload, idempotency_key)
#      values ('provider.api-football.live','provider-fixtures',10,
#              '{"live":true}'::jsonb,'manual:'||now());
# 4. the canonical match id is NOT the provider fixture id; resolve it:
#      select canonical_entity_id from provider_entity_maps
#      where entity_type='match' and provider_entity_id='9900001';
curl -s http://127.0.0.1:18080/v1/matches/<canonical-id> | jq .timeline
```

The assertions worth making, in the order they fail: rows appear at all; a
second poll does **not** duplicate them (identity is enforced by the writer, not
by a unique constraint, so this is the one most likely to be wrong and least
likely to fail loudly); the `Var` row carries `is_retracted`; the 90+3 goal
stores `elapsed_minute=90, elapsed_extra=3`; and each event resolves to a side.

### Two things that look like bugs and are not

**The fixtures are `FT`, with their original dates.** A live status on a fixed
date does not survive: the match-state reaper marks a fixture still claiming to
be in its second half hours after kickoff `unknown`, and the API only reads a
timeline for a live-like or finished match -- so the feature goes quiet and
looks broken. Rewriting the kickoff to "now" on each request is worse: the same
provider fixture id at a new kickoff makes ingestion mint a *second* canonical
match, and QA collected two copies of every mock fixture while this was being
worked out. Finished is terminal and stays readable. The event writer does not
filter on status, so the whole path is still exercised.

**The rate-limit headers claim 75k/day and 450/minute.** They used to claim 100
and 10, which sit below the default reserves of 1500 and 15, so the quota
governor refused every call after the first one with "daily provider quota
reserve reached" -- correct behaviour on numbers the mock had invented. Note
that the governor caches this in memory *and* in `provider_rate_limits`: if a
mock ever advertises a small allowance again, fixing the headers is not enough.
The row has to be corrected and the worker restarted, or it will never call the
provider again to learn better.

### What this cannot test

The in-play **board** lists genuinely live matches, and finished fixtures do not
appear on it. Exercising that needs a real fixture in play, in production.
