import { dirname } from "node:path";
import { fileURLToPath } from "node:url";

import { withSentryConfig } from "@sentry/nextjs";

const appRoot = dirname(fileURLToPath(import.meta.url));

/** @type {import('next').NextConfig} */
const nextConfig = {
  // See apps/web/next.config.mjs: ship the traced server, not the whole
  // node_modules tree.
  output: "standalone",
  allowedDevOrigins: ["127.0.0.1"],
  turbopack: {
    root: appRoot,
  },
};

export default withSentryConfig(nextConfig, {
  org: "sokalens",
  project: "sokalens-admin",
  silent: true,
  telemetry: false,
  disableLogger: true,
  widenClientFileUpload: true,
  sourcemaps: {
    disable: true,
  },
});
