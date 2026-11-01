/**
 * A session's device, as a reader would recognise it.
 *
 * The session list exists so someone can spot a sign-in that is not theirs.
 * Every row read "Unknown device", which makes it useless for exactly that:
 * `device_name` is only set by clients that send one, and of 14 live sessions
 * two had a device name while four had a user agent the API was not returning.
 *
 * A raw user agent is not an answer either -- nobody scans
 * "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:156.0)" and recognises
 * themselves. Browser and platform is what they recognise.
 *
 * Deliberately coarse. This is not device fingerprinting and must not become
 * it: the reader needs enough to say "that is not me", not a catalogue of
 * their hardware.
 */

const BROWSERS: Array<[RegExp, string]> = [
  // Order matters. Edge and Opera both carry "Chrome" in their agent, and
  // Chrome carries "Safari", so the more specific claim has to win.
  [/\bEdgA?\//, "Edge"],
  [/\bOPR\/|\bOpera\//, "Opera"],
  [/\bSamsungBrowser\//, "Samsung Internet"],
  [/\bFirefox\/|\bFxiOS\//, "Firefox"],
  [/\bCriOS\//, "Chrome"],
  [/\bChrome\//, "Chrome"],
  [/\bSafari\//, "Safari"],
];

const PLATFORMS: Array<[RegExp, string]> = [
  [/\biPhone\b/, "iPhone"],
  [/\biPad\b/, "iPad"],
  [/\bAndroid\b/, "Android"],
  [/\bWindows NT\b/, "Windows"],
  [/\bMac OS X\b|\bMacintosh\b/, "Mac"],
  [/\bCrOS\b/, "ChromeOS"],
  [/\bLinux\b/, "Linux"],
];

function firstMatch(value: string, table: Array<[RegExp, string]>): string | null {
  for (const [pattern, label] of table) {
    if (pattern.test(value)) return label;
  }
  return null;
}

export function deviceLabel(deviceName?: string | null, userAgent?: string | null): string {
  // A client that sent its own name knows better than anything inferred here.
  const named = (deviceName ?? "").trim();
  if (named) return named;

  const agent = (userAgent ?? "").trim();
  if (!agent) {
    // Honest rather than invented. A session with neither field is genuinely
    // unidentified, and saying so is better than guessing at one.
    return "Unknown device";
  }

  const browser = firstMatch(agent, BROWSERS);
  const platform = firstMatch(agent, PLATFORMS);

  if (browser && platform) return `${browser} on ${platform}`;
  if (browser) return browser;
  if (platform) return platform;

  // Something we do not recognise -- a scripted client, a new browser. Show a
  // short prefix so it is at least distinguishable from the other rows, rather
  // than collapsing every unrecognised agent into one indistinguishable label.
  return agent.length > 40 ? `${agent.slice(0, 40)}…` : agent;
}
