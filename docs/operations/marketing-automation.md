# Marketing automation

The worker posts the daily distribution routine by itself: the morning picks,
the evening results, goals in big matches, the weekly record, and the Sunday
numbers. What a channel's API does not allow, it prepares and sends to you on
Telegram, ready to forward.

Code: `services/worker/internal/marketing` (scheduling, content, channels) and
`apps/web/app/og/campaign/route.tsx` with `apps/web/app/lib/campaignCard.tsx`
(the card images). Ledger: the `marketing_posts` table.

## What runs, and when

Times are `MARKETING_TIMEZONE` (default Nairobi).

| When | Post | Goes to |
| --- | --- | --- |
| Daily from 08:00 | Today's picks card: the 3 most confident published picks in the covered leagues, one per match, never a best bet | Telegram channel, Facebook Page, and you (for WhatsApp) |
| Right after | Story pack: the picks card plus a "before kick-off" card per match, in 9:16, and a video of them | You (for WhatsApp Status, TikTok, Reels, Shorts, Instagram Story) |
| Each evening, once every pick is graded (or 6 hours after the last kick-off) | Results card: every pick, won, lost, void or pending | Telegram channel, Facebook Page, and you (for WhatsApp) |
| Live, every 2 minutes | Goal card with live win chances, for matches in the day's picks and big leagues; a correction if VAR removes a goal | Telegram channel (up to 15 a day) |
| Monday 09:00 | Last week's record card | Telegram channel, Facebook Page, and you |
| Thursday 10:00 | Draft data post (one market's 90-day record, rotating weekly) | You, to check and post |
| Sunday 20:00 | Weekly numbers: accounts, returning accounts, visitors by source, Telegram members, posts | You |

Still by hand, because no API allows it or because automating it would get
accounts banned: forwarding to the WhatsApp Channel, uploading to TikTok, posting
in groups, replying to people, and outreach.

## Setup

1. **Telegram bot.** In Telegram, message `@BotFather`, send `/newbot`, and keep the token.
2. **Your owner chat.** Send your bot any message, then open
   `https://api.telegram.org/bot<token>/getUpdates` in a browser and read
   `message.chat.id`. That number is `MARKETING_TELEGRAM_OWNER_CHAT_ID`.
3. **The channel.** Create the public channel (for example `@sokalens`) and add
   the bot as an administrator that can post messages.
4. **Facebook Page (optional).** Create a Meta app, give it `pages_manage_posts`
   for your Page, and generate a long-lived Page access token. Without one,
   Facebook is simply skipped.
5. **Card secret.** Run `openssl rand -hex 32`. The same value goes in the
   worker's and the web app's environment as `MARKETING_CARD_SECRET`.
6. **Leagues.** Picks come from leagues with `priority_score` of 60 or more, plus
   any ids in `MARKETING_LEAGUE_IDS`. To add the Kenyan Premier League, find its
   id with `SELECT id, name FROM leagues WHERE country_code = 'KE';`.
7. Set the variables in `.env.prod` (all are listed in `.env.example`), and
   check that `.env.prod` has `APP_ENV=production`: live mode is refused
   anywhere else.

## Turning it on

1. `MARKETING_MODE=preview` and deploy. For a day or two everything goes only
   to your owner chat, labelled `[PREVIEW]`. Check the cards, the captions and
   the timing.
2. `MARKETING_MODE=live` and deploy. Posts now go to the channel and Page.
3. Kill switch: `MARKETING_MODE=off` and restart the worker. Nothing is
   scheduled while it is off.

QA must stay `off` or `preview`. Live mode in QA is held to preview by the code,
but never copy the production bot token or channel into `.env.qa`.

## Rules the code enforces

- Only published predictions, with the public feed's filters. Best bets are
  excluded, because they are gated to accounts.
- Every caption ends with "18+ · Analysis, not betting advice. SokaLens is not
  a bookmaker", and every card carries the same in its footer.
- Results are graded from the stored settlement, or from the final score by the
  engine's own rules. Losses are always shown.
- A retracted goal gets a visible correction; the original post is not deleted.
- Every link carries `utm_source` (telegram, facebook, whatsapp) so
  `visitor_acquisition` shows which channel works.
- Each post is written to the ledger before it is sent, so a retry cannot post
  twice. A failed post is retried up to 3 times, then you are told.

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| Worker log: `marketing disabled: invalid configuration` | A missing or malformed variable; the log names it. The worker keeps running without marketing. |
| Worker log: `marketing held to preview` | `MARKETING_MODE=live` without `APP_ENV=production`. |
| Posts fail with `the web route did not verify the token` | `MARKETING_CARD_SECRET` differs between web and worker, or is missing on web. |
| No morning card, and you got "No published picks in the covered leagues today" | No eligible pick in the covered leagues. Widen `MARKETING_LEAGUE_IDS` or lower `MARKETING_MIN_LEAGUE_PRIORITY`. |
| No video, only the story images | ffmpeg is missing from the worker image, or the build failed; you are told which. |
| A post to check: | `SELECT post_key, channel, status, attempts, last_error FROM marketing_posts ORDER BY id DESC LIMIT 20;` |
