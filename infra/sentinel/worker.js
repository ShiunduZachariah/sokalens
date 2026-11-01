/**
 * SokaLens out-of-band sentinel.
 *
 * A Telegram sender inside ops-controller cannot report that ops-controller is
 * dead. That is the single most important limitation of the whole ops bridge,
 * and this Worker is the answer to it: it runs on Cloudflare's edge, outside
 * the VPS failure domain, and calls the Telegram API directly.
 *
 * Deliberate properties:
 *
 *   - Its own bot token, stored as a Worker secret and never shared with the
 *     controller. If the controller's token leaks, this path still works.
 *   - Every message is prefixed OUT-OF-BAND, so in-band and out-of-band alerts
 *     are distinguishable at a glance at 3am.
 *   - It fires only after 3 consecutive failed runs, so one flaky check does
 *     not page anyone.
 *   - It also sends email. A Telegram outage during a VPS outage would
 *     otherwise leave nothing at all.
 *
 * Secrets (wrangler secret put ...): SENTINEL_BOT_TOKEN, SENTINEL_CHAT_ID,
 * RESEND_API_KEY, ALERT_EMAIL_TO, ALERT_EMAIL_FROM.
 * Binding: SENTINEL_KV (a KV namespace, for the consecutive-failure counter).
 */

const CHECKS = ["https://api.sokalens.com/healthz", "https://sokalens.com/"];
const FAILURE_THRESHOLD = 3;
const CHECK_TIMEOUT_MS = 10_000;
const FAILURE_KEY = "consecutive_failures";
// Expire the counter well after the cron interval so a long outage keeps
// counting, but a Worker that stops running eventually forgets.
const FAILURE_TTL_SECONDS = 3600;

export default {
  async scheduled(event, env, ctx) {
    const results = await Promise.allSettled(
      CHECKS.map((url) =>
        fetch(url, {
          signal: AbortSignal.timeout(CHECK_TIMEOUT_MS),
          cf: { cacheTtl: 0 },
          headers: { "User-Agent": "sokalens-sentinel/1" },
        })
      )
    );

    const down = results.filter(
      (r) => r.status === "rejected" || !r.value.ok
    );

    if (down.length === 0) {
      await env.SENTINEL_KV.delete(FAILURE_KEY);
      return;
    }

    const failures = await incrementFailureCount(env);
    if (failures < FAILURE_THRESHOLD) {
      // Not yet convincing. Counting without alerting is the point.
      return;
    }

    const text =
      `\u{1F6A8} OUT-OF-BAND\n\n` +
      `SokaLens unreachable from Cloudflare.\n` +
      `${down.length}/${CHECKS.length} checks failing for ${failures} consecutive runs.\n\n` +
      `This message did not come from ops-controller.`;

    // Both channels, independently. Neither failing may prevent the other.
    const [telegram, email] = await Promise.allSettled([
      sendTelegram(env, text),
      sendEmail(env, text),
    ]);

    if (telegram.status === "rejected" && email.status === "rejected") {
      // Nothing reached anyone. Surface it in the Worker logs, which is the
      // only channel left.
      console.error("sentinel: both alert channels failed", {
        telegram: String(telegram.reason),
        email: String(email.reason),
      });
    }
  },
};

async function incrementFailureCount(env) {
  const current = parseInt((await env.SENTINEL_KV.get(FAILURE_KEY)) ?? "0", 10);
  const next = Number.isFinite(current) ? current + 1 : 1;
  await env.SENTINEL_KV.put(FAILURE_KEY, String(next), {
    expirationTtl: FAILURE_TTL_SECONDS,
  });
  return next;
}

async function sendTelegram(env, text) {
  const response = await fetch(
    `https://api.telegram.org/bot${env.SENTINEL_BOT_TOKEN}/sendMessage`,
    {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ chat_id: env.SENTINEL_CHAT_ID, text }),
      signal: AbortSignal.timeout(CHECK_TIMEOUT_MS),
    }
  );
  if (!response.ok) {
    // Never include the URL: it carries the bot token.
    throw new Error(`telegram sendMessage failed with ${response.status}`);
  }
}

async function sendEmail(env, text) {
  if (!env.RESEND_API_KEY || !env.ALERT_EMAIL_TO) {
    throw new Error("email not configured");
  }
  const response = await fetch("https://api.resend.com/emails", {
    method: "POST",
    headers: {
      Authorization: `Bearer ${env.RESEND_API_KEY}`,
      "Content-Type": "application/json",
    },
    body: JSON.stringify({
      from: env.ALERT_EMAIL_FROM,
      to: [env.ALERT_EMAIL_TO],
      subject: "OUT-OF-BAND: SokaLens unreachable",
      text,
    }),
    signal: AbortSignal.timeout(CHECK_TIMEOUT_MS),
  });
  if (!response.ok) {
    throw new Error(`resend failed with ${response.status}`);
  }
}
