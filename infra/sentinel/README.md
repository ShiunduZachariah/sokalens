# Out-of-band sentinel

A Telegram sender inside `ops-controller` **cannot report that `ops-controller`
is dead.** This Cloudflare Worker is the answer to that: it runs outside the VPS
failure domain and calls the Telegram API directly.

## Why it is separate from everything else

| Property | Reason |
|---|---|
| Its **own** bot token | If the controller's token leaks or is rotated, the break-glass path still works. Never reuse `OPS_TELEGRAM_BOT_TOKEN` here. |
| `🚨 OUT-OF-BAND` prefix | In-band and out-of-band alerts must be distinguishable instantly. If a message has this prefix, the controller did not send it — and may be down. |
| 3 consecutive failures | One flaky check from one edge location is not an outage. At a 5-minute cron this pages in ~15 minutes. |
| Email **as well as** Telegram | A Telegram outage during a VPS outage would otherwise leave nothing. Both are attempted independently; neither failing blocks the other. |
| Deployed on its own | Sharing a deploy step with the stack it watches would mean a broken deploy takes out the thing that reports broken deploys. |

## Setup

```bash
cd infra/sentinel
wrangler kv namespace create SENTINEL_KV     # paste the id into wrangler.toml
```

Create a **second** bot with BotFather, add it to the same ops chat, then:

```bash
wrangler secret put SENTINEL_BOT_TOKEN
wrangler secret put SENTINEL_CHAT_ID
wrangler secret put RESEND_API_KEY
wrangler secret put ALERT_EMAIL_TO
```

```bash
wrangler deploy
```

## Verifying it works

This is the one alert path that cannot be tested by waiting for it to be
needed. Test it deliberately, per the spec's manual checklist:

```bash
wrangler tail
```

Then stop the stack and confirm a message arrives that is prefixed
`🚨 OUT-OF-BAND`, and that an email arrives too:

```bash
docker compose -f infra/docker/docker-compose.yml --profile core stop api web
```

Restart afterwards, and confirm the KV counter clears on the next healthy run.

## What it deliberately does not do

It performs no remediation. It has no database access, no deploy rights and no
runbooks. Its only job is to tell a human that the thing which normally tells
them things is not answering.
