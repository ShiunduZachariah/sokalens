import { dateHubUrls } from "../../lib/sitemapSources";
import { urlSet } from "../../lib/sitemapXml";

export function GET() {
  return urlSet(dateHubUrls);
}
