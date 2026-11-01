import { existsSync } from "node:fs";
import { defineConfig, devices } from "@playwright/test";

/**
 * End-to-end configuration.
 *
 * ## Why there is a `webServer` block at all
 *
 * The one spec that existed before this file pointed at
 * `http://host.docker.internal:3000` and assumed somebody had already started
 * the app. That works on a developer's machine with the compose stack up and
 * nowhere else -- in CI, or in any container without Docker, the suite failed
 * on connection refused, which reads as a broken test rather than a missing
 * server. Playwright starts both processes here and stops them when the run
 * ends, which is also what the local runtime policy asks for: nothing
 * long-lived is left behind.
 *
 * `PLAYWRIGHT_BASE_URL` still wins. A developer with the stack already running
 * sets it and no server is started, so the old workflow is unchanged.
 *
 * ## Why the app is built, not run in dev mode
 *
 * `next dev` compiles each route on first request, so the first navigation in
 * every spec pays several seconds and times out on a cold cache. `next build`
 * once, then `next start`, makes the run both faster and representative of what
 * a reader gets.
 *
 * ## Why the API is stubbed
 *
 * See `tests/stub-api/server.mjs`. Without it every page renders its empty
 * state and a browser test proves only that the shells render.
 */
/**
 * Where Chromium is, when it is not where Playwright expects.
 *
 * `@playwright/test` resolves a browser by the build number it was published
 * against, so a pre-provisioned image whose Chromium is a different build fails
 * with "Executable doesn't exist" and advises `npx playwright install` -- which
 * is exactly what such an image sets `PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD` to
 * prevent. Pointing at the browser that IS there is the fix, and it is a no-op
 * on a machine where Playwright manages its own download.
 */
const chromiumPath = process.env.PLAYWRIGHT_CHROMIUM_PATH
  ?? ["/opt/pw-browsers/chromium"].find((candidate) => existsSync(candidate));

const externalBaseUrl = process.env.PLAYWRIGHT_BASE_URL;
const port = Number(process.env.PLAYWRIGHT_PORT ?? 3100);
const stubPort = Number(process.env.STUB_API_PORT ?? 8099);
const baseURL = externalBaseUrl ?? `http://127.0.0.1:${port}`;

export default defineConfig({
  testDir: "./tests",
  // One worker: both servers are shared, and the fixtures are deliberately
  // deterministic rather than isolated per worker. Parallel specs hitting one
  // stub would still pass, but a failure would be harder to reproduce.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  // A retry hides a flake rather than fixing it. The fixtures are deterministic
  // and the servers are local, so a failure here is a real failure.
  retries: 0,
  reporter: process.env.CI ? [["list"], ["json", { outputFile: "playwright-report.json" }]] : "list",
  timeout: 60_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL,
    // Chromium only. The pages are tested for layout and behaviour, not for
    // engine differences, and a three-browser matrix triples the run for
    // findings this suite is not built to produce.
    ...devices["Desktop Chrome"],
    ...(chromiumPath ? { launchOptions: { executablePath: chromiumPath } } : {}),
    trace: "retain-on-failure",
    screenshot: "only-on-failure",
  },
  webServer: externalBaseUrl ? undefined : [
    {
      command: `node tests/stub-api/server.mjs`,
      url: `http://127.0.0.1:${stubPort}/healthz`,
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      timeout: 30_000,
    },
    {
      command: `npx next build && npx next start --port ${port}`,
      url: baseURL,
      reuseExistingServer: !process.env.CI,
      stdout: "pipe",
      // A cold `next build` of this app takes a couple of minutes.
      timeout: 600_000,
      env: {
        PREDICTION_API_BASE_URL: `http://127.0.0.1:${stubPort}`,
        // Production has this on. Without it the homepage's featured ACCAs
        // section renders nothing and `/api/accas` answers 503, so the
        // generator and three of the pre-existing specs fail on a flag rather
        // than on anything they are testing.
        ACCA_INVENTORY_V2_ENABLED: "true",
        // The real deploy sets this; without it the pages that read it fall
        // back to localhost:8080 and the stub is bypassed silently.
        NEXT_PUBLIC_SITE_URL: baseURL,
      },
    },
  ],
});
