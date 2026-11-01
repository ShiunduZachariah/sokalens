import { dirname } from "node:path";
import { fileURLToPath } from "node:url";


const appRoot = dirname(fileURLToPath(import.meta.url));

/** @type {import('next').NextConfig} */
const nextConfig = {
  // Emit a self-contained server with only the files the app actually imports,
  // instead of shipping the whole node_modules tree into the runtime image.
  // The production image was copying every dependency, dev tooling included.
  output: "standalone",
  allowedDevOrigins: ["127.0.0.1"],
  async redirects() {
    return [
      {
        source: "/football/today",
        destination: "/matches/today",
        permanent: true,
      },
      {
        source: "/leagues/:slug",
        destination: "/predictions/leagues/:slug",
        permanent: true,
      },
      {
        // `/performance` is live and 404s. Nothing in this app links it, so the
        // links are external or historical -- which is exactly the case a 301 is
        // for. The settled record lives at /results; the design calls the page
        // "How we performed", and that wording is almost certainly where the
        // path came from.
        source: "/performance",
        destination: "/results",
        permanent: true,
      },
    ];
  },
  turbopack: {
    root: appRoot,
  },
};

export default nextConfig;
