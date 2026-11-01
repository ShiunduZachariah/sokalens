import { staticUrls } from "../../lib/sitemapSources";
import { urlSet } from "../../lib/sitemapXml";

export function GET() {
  return urlSet(staticUrls);
}
