import { campaignCard } from "../../lib/campaignCard";
import { parseCampaignToken } from "../../lib/campaignToken";
import { brandShareCard } from "../../lib/shareCard";

/**
 * The image for one automated post (services/worker/internal/marketing).
 *
 * The worker signs the card's content and fetches the PNG from here over the
 * Compose network, then uploads the bytes to Telegram and Facebook itself, so
 * no channel ever has to reach this URL through Cloudflare.
 *
 * A token that does not verify gets the plain brand card and no
 * `X-Campaign-Card` header. The worker refuses to post anything without that
 * header, so a missing or mismatched MARKETING_CARD_SECRET fails loudly in the
 * worker log instead of putting a generic image in a public channel.
 */
export const runtime = "nodejs";

export async function GET(request: Request) {
  const token = new URL(request.url).searchParams.get("t");
  const card = parseCampaignToken(token, process.env.MARKETING_CARD_SECRET);
  if (!card) {
    return brandShareCard("Football predictions with the reasoning kept in view.");
  }
  return campaignCard(card, {
    "X-Campaign-Card": "verified",
    // A token is its content: the same token always draws the same image.
    "Cache-Control": "public, max-age=31536000, immutable",
    "X-Robots-Tag": "noindex",
  });
}
