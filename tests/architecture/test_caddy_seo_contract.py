from pathlib import Path
import unittest


ROOT = Path(__file__).resolve().parents[2]


class CaddySeoContractTests(unittest.TestCase):
    def test_www_host_permanently_redirects_to_apex_before_reverse_proxying(self) -> None:
        caddyfile = (ROOT / "infra" / "deploy" / "Caddyfile").read_text(encoding="utf-8")

        self.assertNotIn("sokalens.com, www.sokalens.com", caddyfile)
        self.assertIn("www.sokalens.com {", caddyfile)
        self.assertIn("redir https://sokalens.com{uri} permanent", caddyfile)

    def test_ci_deploy_validates_installs_reloads_and_rolls_back_caddy(self) -> None:
        deploy_script = (ROOT / "infra" / "deploy" / "ci-deploy.sh").read_text(encoding="utf-8")

        self.assertIn('sudo caddy validate --config "$CADDY_SOURCE"', deploy_script)
        self.assertIn('sudo install -m 0644 "$CADDY_SOURCE" "$CADDY_LIVE_CONFIG"', deploy_script)
        self.assertIn("sudo systemctl reload caddy", deploy_script)
        self.assertIn('restore_caddy_config', deploy_script)

    def test_reverse_proxies_retry_safe_reads_through_container_restarts(self) -> None:
        caddyfile = (ROOT / "infra" / "deploy" / "Caddyfile").read_text(encoding="utf-8")

        # Apex account/API reads plus the two operator frontends all tolerate a
        # short container replacement without exposing a transient 502.
        self.assertEqual(caddyfile.count("lb_try_duration 15s"), 4)
        self.assertEqual(caddyfile.count("lb_try_interval 250ms"), 4)

    def test_proxy_only_changes_do_not_rebuild_the_entire_stack(self) -> None:
        workflow = (ROOT / ".github" / "workflows" / "ci.yml").read_text(encoding="utf-8")
        deploy_script = (ROOT / "infra" / "deploy" / "ci-deploy.sh").read_text(encoding="utf-8")

        self.assertIn("proxy: ${{ steps.baseline.outputs.full == 'true' || steps.filter.outputs.proxy }}", workflow)
        self.assertIn("SURFACE_PROXY: ${{ needs.detect.outputs.proxy }}", workflow)
        self.assertIn('SERVICES="proxy-only"', workflow)
        self.assertIn('if [ "$SERVICE_LIST" = "proxy-only" ]', deploy_script)
        self.assertIn('if [ -n "$SERVICES" ]; then', deploy_script)

    def test_edge_cache_covers_anonymous_pages_and_excludes_personalised_ones(self) -> None:
        """The edge may cache a page only if every viewer gets the same HTML.

        Next marks dynamic renders `private, no-cache`, so Cloudflare caches
        nothing and every visitor pays a full round trip to the origin. Caddy
        overrides that for pages whose server render is anonymous. Nothing in
        the app reads a session cookie server-side, so signed-in and signed-out
        visitors receive identical HTML; the pages that read the *timezone*
        cookie genuinely differ and must never be cached.
        """
        import re

        caddyfile = (ROOT / "infra" / "deploy" / "Caddyfile").read_text(encoding="utf-8")

        self.assertIn("@cacheableHtml", caddyfile)
        # `>` is set-and-replace. Plain `header` appends, so the response
        # carries both this and Next's `private, no-cache, no-store`, and
        # caches honour no-store -- the directive looks applied and does
        # nothing. Verified against production the first time it shipped.
        long_header = re.search(
            r'header @cacheableHtml >Cache-Control "public, max-age=0, s-maxage=(\d+), stale-while-revalidate=(\d+)"',
            caddyfile,
        )
        self.assertIsNotNone(long_header)
        self.assertLessEqual(int(long_header.group(1)), 300, "edge TTL outlives a failed purge")
        self.assertLessEqual(int(long_header.group(2)), 120, "stale window outlives a deploy")
        # A client-side navigation gets a different body for the same URL and
        # Cloudflare cannot vary on the header that distinguishes them.
        self.assertIn("not header RSC *", caddyfile)

        # A cached document names the exact JS chunk hashes of the build that
        # produced it. A deploy replaces those files, so any window in which
        # the edge may serve stale HTML after a release is a window in which
        # visitors get markup pointing at JavaScript that 404s -- the page
        # renders from SSR but nothing hydrates. Keep the window small enough
        # that a bad minute cannot become a bad hour.
        # The two deferred Caddy header handlers can both match one response.
        # In that case the generic header wins, even when @liveBearing is
        # declared later. Check the effective route choice, including the
        # generic exclusion, instead of checking either regex alone.
        long_block = caddyfile.split("@cacheableHtml {", 1)[1].split("\t}", 1)[0]
        live_block = caddyfile.split("@liveBearing {", 1)[1].split("\t}", 1)[0]
        long_path = re.compile(re.search(r"(?m)^\s*path_regexp (\S+)", long_block).group(1))
        long_exclusion = re.search(r"(?m)^\s*not path_regexp (\S+)", long_block).group(1)
        live_path = re.search(r"(?m)^\s*path_regexp (\S+)", live_block).group(1)
        self.assertEqual(long_exclusion, live_path, "long TTL must exclude every live-bearing path")
        live_path = re.compile(live_path)

        def policy_for(path: str) -> str | None:
            long_match = bool(long_path.match(path)) and not bool(live_path.match(path))
            live_match = bool(live_path.match(path))
            self.assertFalse(long_match and live_match, f"{path} matches both cache headers")
            return "long" if long_match else "live" if live_match else None

        for path in (
            "/",
            "/best-bets",
            "/leagues",
            "/leagues/premier-league",
            "/teams/arsenal",
            "/predictions/leagues/premier-league",
            "/predictions/premier-league/arsenal-vs-chelsea-852838",
            "/es/teams/arsenal",
            "/de/predictions/premier-league/arsenal-vs-chelsea-852838",
        ):
            self.assertIsNotNone(policy_for(path), f"{path} should be edge-cacheable")

        for path in ("/", "/leagues", "/leagues/premier-league",
                     "/predictions/premier-league/arsenal-vs-chelsea-852838",
                     "/de/predictions/premier-league/arsenal-vs-chelsea-852838",
                     "/predictions/leagues/serie-a-12345"):
            self.assertEqual(policy_for(path), "live", f"{path} must get the 15s header")

        for path in ("/best-bets", "/teams/arsenal", "/predictions/leagues/premier-league"):
            self.assertEqual(policy_for(path), "long", f"{path} should keep the bounded long TTL")

        for path in (
            # Each of these reads a cookie and renders per viewer.
            "/predictions/today",
            "/predictions/tomorrow",
            "/predictions/weekend",
            "/predictions/markets/over-2-5-goals",
            "/matches/today",
            "/results",
            "/acca-predictions",
            "/es/predictions/today",
            # Account surfaces must never be cached, cookie or not.
            "/account",
            "/login",
            "/favorites",
            "/api/account/v1/access",
        ):
            self.assertIsNone(policy_for(path), f"{path} must NOT be edge-cacheable")

    def test_origin_records_who_asked_for_what(self) -> None:
        """The origin had no access log, and the site had no client beacon.

        With neither, a quiet day and a broken funnel are indistinguishable --
        which is exactly the position this property was in. Edge caching makes
        the origin log less complete, not less necessary: it is the only record
        of the requests Cloudflare did not absorb.
        """
        caddyfile = (ROOT / "infra" / "deploy" / "Caddyfile").read_text(encoding="utf-8")

        self.assertIn("/var/log/caddy/sokalens-access.log", caddyfile)
        self.assertIn("format json", caddyfile)
        # Unbounded logs fill the disk that serves the site.
        self.assertIn("roll_size", caddyfile)
        self.assertIn("roll_keep", caddyfile)
        # Immutable assets are answered at the edge and would bury the signal.
        self.assertIn("log_skip", caddyfile)

        # Bounded three ways, because a log that fills the disk serving the
        # site is worse than no log. Retention also has a backstop in the
        # post-deploy GC, in case a rotation setting is ever edited wrongly.
        self.assertIn("roll_keep_for", caddyfile)
        deploy_script = (ROOT / "infra" / "deploy" / "ci-deploy.sh").read_text(encoding="utf-8")
        self.assertIn("prune_rotated_logs", deploy_script)
        self.assertIn("ACCESS_LOG_KEEP_DAYS", deploy_script)

    def test_pages_that_can_show_a_live_score_are_not_cached_for_minutes(self) -> None:
        """A fixture page renders the score server-side with no client poller.

        The data layer refreshes it every 15 seconds. A 300s edge cache in
        front of that serves a five-minute-old scoreline during a match, which
        is worse than not caching it. The edge TTL for anything that can show
        an in-play score must not exceed the origin's own freshness contract.
        """
        import re

        caddyfile = (ROOT / "infra" / "deploy" / "Caddyfile").read_text(encoding="utf-8")

        self.assertIn("@liveBearing", caddyfile)
        live = re.search(r"header @liveBearing >Cache-Control \"[^\"]*s-maxage=(\d+)", caddyfile)
        self.assertIsNotNone(live, "live-bearing pages have no edge TTL of their own")
        self.assertLessEqual(int(live.group(1)), 15, "edge TTL outlives the origin's 15s revalidate")

        block = caddyfile.split("@liveBearing {", 1)[1].split("\t}", 1)[0]
        self.assertIn("method GET HEAD", block)
        self.assertIn("not header RSC *", block)
        self.assertIn("not header Next-Router-Prefetch *", block)
        matcher = re.compile(re.search(r"(?m)^\s*path_regexp (\S+)", block).group(1))
        for path in ("/", "/leagues", "/leagues/serie-a",
                     "/predictions/serie-a/inter-vs-milan-12345",
                     "/es/predictions/serie-a/inter-vs-milan-12345"):
            self.assertIsNotNone(matcher.match(path), f"{path} can show a live score and needs the short TTL")
        # Evergreen pages keep the long TTL; nothing on them ticks.
        for path in ("/about", "/terms", "/teams/arsenal"):
            self.assertIsNone(matcher.match(path), f"{path} should keep the long edge TTL")

if __name__ == "__main__":
    unittest.main()


class EdgeCacheEnablementTests(unittest.TestCase):
    """Cloudflare does not cache HTML without an explicit Cache Rule.

    Caddy sent a correct `s-maxage=300` for weeks and every page still came back
    `cf-cache-status: DYNAMIC`, because eligibility is a separate decision the
    origin cannot make. cloudflare-cache-rule.sh makes it. Measured after it was
    applied: /best-bets went MISS -> HIT and TTFB 0.80s -> 0.34s, while
    /predictions/today and /account stayed DYNAMIC as they must.
    """

    def test_the_cache_rule_script_bypasses_navigations_before_it_caches(self) -> None:
        script = (ROOT / "infra" / "deploy" / "cloudflare-cache-rule.sh").read_text(encoding="utf-8")

        # Cloudflare evaluates rules in order and the first match wins, so the
        # bypass has to be written before the cache rule, not merely present.
        # Next serves a different body for the same URL to client-side
        # navigations; serving one as the other breaks routing.
        self.assertLess(
            script.index('"cache": False'),
            script.index('"cache": True'),
            "the RSC bypass must precede the cache rule or navigations get cached",
        )
        self.assertIn("next-router-prefetch", script)

    def test_the_origin_decides_what_is_actually_stored(self) -> None:
        """The rule grants eligibility; Cache-Control still governs.

        This is why the script does not carry a copy of the Caddyfile path list.
        Caddy sends a cacheable header only for @cacheableHtml and every other
        page answers Next's `private, no-cache, no-store`, which the edge honours
        by storing nothing. Duplicating the list would create two lists to drift.
        """
        script = (ROOT / "infra" / "deploy" / "cloudflare-cache-rule.sh").read_text(encoding="utf-8")

        self.assertIn('"edge_ttl": {"mode": "respect_origin"}', script)
        self.assertIn('"browser_ttl": {"mode": "respect_origin"}', script)

    def test_personalised_surfaces_are_excluded_explicitly(self) -> None:
        """Belt as well as braces on the pages where a mistake is serious.

        Respecting the origin should be enough on its own, but these are the
        paths where a wrongly-set header would serve one reader another reader's
        page, so they are named in the rule as well.
        """
        script = (ROOT / "infra" / "deploy" / "cloudflare-cache-rule.sh").read_text(encoding="utf-8")

        for personalised in ("/account", "/login", "/results", "/predictions/today", "/predictions/markets/"):
            self.assertIn(f'"{personalised}"', script, f"{personalised} may be cached at the edge")

        # `contains` covers every locale prefix in one clause. Regex is not
        # available: the `matches` operator requires a Business plan, which this
        # zone is not on -- an earlier version of this rule was rejected for it.
        self.assertIn("contains", script)
        # The operator, not the word: this file explains in prose why regex is
        # unavailable, and "the first match wins" appears in a comment.
        self.assertNotIn('path matches', script)

    def test_the_script_is_idempotent_and_never_prints_the_token(self) -> None:
        script = (ROOT / "infra" / "deploy" / "cloudflare-cache-rule.sh").read_text(encoding="utf-8")

        # A zone's cache ruleset already exists as an entrypoint, so a second run
        # must update it rather than add another.
        self.assertIn("PUT", script)
        self.assertIn("http_request_cache_settings", script)
        self.assertIn("CLOUDFLARE_API_TOKEN", script)
        self.assertNotIn('echo "$CF_TOKEN"', script)
