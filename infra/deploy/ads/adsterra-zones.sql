-- Configure and enable the Adsterra provider. RUN THIS DELIBERATELY, per
-- environment, as an operator. It is not a migration and must not become one.
--
-- WHY NOT A MIGRATION. 000082 seeds the ad catalogue disabled and unconfigured
-- on purpose, so that a schema change can never start spending a reader's
-- attention or a provider's budget. `scripts/architecture/verify_additive_migrations.py`
-- enforces exactly that: after every migration it asserts the three providers
-- are `3:0:3` (three rows, none enabled, all DISABLED) and that no row carries
-- public configuration. Two migrations that set zones and flipped `enabled`
-- were written first and that gate caught them, which is the gate working.
--
-- WHAT IS SAFE TO PUT HERE. `public_config` is browser-safe by contract: a
-- pinned script origin and one zone per slot format. The 32-hex strings are
-- Adsterra zone ids -- public identifiers that ship in the page source, not
-- secrets. The publisher API token is a credential and lives in the host env
-- file as ADSTERRA_API_TOKEN; it must never appear here.
--
-- The web adapter builds each script URL from the origin, the unit kind and
-- the zone id, and refuses an origin that is not on its own allowlist. Nothing
-- written here can contribute a path, a URL or markup.
--
-- HOW TO RUN IT
--
--   production:
--     docker exec -i sports-prediction-platform-postgres-1 \
--       psql -U sports -d sports_prediction -v ON_ERROR_STOP=1 \
--       < infra/deploy/ads/adsterra-zones.sql
--
--   QA (rootless docker, as the dev user):
--     sudo -u dev docker exec -i sokalens-dev-postgres-1 \
--       psql -U sports -d sports_prediction_qa -v ON_ERROR_STOP=1 \
--       < infra/deploy/ads/adsterra-zones.sql
--
-- TO STOP SERVING, without losing the zones:
--
--   UPDATE ad_providers SET enabled = FALSE, health_status = 'DISABLED',
--          traffic_weight = 0, updated_at = now() WHERE code = 'adsterra';
--
-- THE DASHBOARD'S OTHER IDENTIFIERS, for reconciliation. These are unit ids
-- under site 6093931; they identify a unit in Adsterra's dashboard and
-- publisher API and render nothing, which is why they are here as a comment
-- and not in public_config:
--
--   31530412  NativeBanner_1  63f75d6b...   31530415  160x600_1  f740d40b...
--   31530413  SocialBar_1     b3469c1e...   31530416  728x90_1   bc5283f8...
--   31530414  300x250_1       d1f0d90b...   31530417  320x50_1   a540f124...
--
-- The Social Bar is deliberately absent. It injects its own fixed element over
-- the page, so it cannot collapse when unfilled the way every other placement
-- does, and on mobile it lands on the adhesion band the design already
-- occupies. Its unit stays live in the dashboard; nothing here serves it.

BEGIN;

UPDATE ad_providers
SET public_config = jsonb_build_object(
      'script_origin', 'https://bauval.org',
      'zones', jsonb_build_object(
        -- 728x90 leaderboard
        'top',      jsonb_build_object('kind', 'banner', 'zone_id', 'bc5283f86ffa8d1825f26b6ed0890189', 'width', 728, 'height', 90),
        'banner',   jsonb_build_object('kind', 'banner', 'zone_id', 'bc5283f86ffa8d1825f26b6ed0890189', 'width', 728, 'height', 90),
        -- The native widget's one safe home: `footer` is rendered once per
        -- page, which is the only way a unit keyed by a fixed element id can
        -- be used at all.
        'footer',   jsonb_build_object('kind', 'native', 'zone_id', '63f75d6bc7a2d8e0cdd87f0217d2a8be'),
        -- 300x250, and it has to be a BANNER here, not the native widget.
        -- A feed repeats ad blocks every ten rows, two across above 880px,
        -- requesting each block only as it approaches the viewport. The native
        -- widget writes into an element it finds by a fixed zone-derived id,
        -- so every subsequent copy on a page duplicates that id and silently
        -- fails after burning its timeout.
        -- Banner scripts run serially in the page document, so this zone can
        -- be repeated at distinct placements without racing `atOptions`.
        'in-feed',  jsonb_build_object('kind', 'banner', 'zone_id', 'd1f0d90bc1562d7b39503f9a38c8a85e', 'width', 300, 'height', 250),
        -- 160x600 skyscraper for the desktop rail
        'rail',     jsonb_build_object('kind', 'banner', 'zone_id', 'f740d40b7a5d515ebb1e37f58d834164', 'width', 160, 'height', 600),
        'sidebar',  jsonb_build_object('kind', 'banner', 'zone_id', 'f740d40b7a5d515ebb1e37f58d834164', 'width', 160, 'height', 600),
        -- 320x50 adhesion above the mobile tab bar
        'adhesion', jsonb_build_object('kind', 'banner', 'zone_id', 'a540f12426274972ff1d7bc65485413a', 'width', 320, 'height', 50),
        -- 300x250 rectangle. The pre-refresh `inline` slot is a fixed box in a
        -- fixed column, which is what this unit is.
        'inline',   jsonb_build_object('kind', 'banner', 'zone_id', 'd1f0d90bc1562d7b39503f9a38c8a85e', 'width', 300, 'height', 250)
      )
    ),
    updated_at = now()
WHERE code = 'adsterra';

-- Three columns decide eligibility together, and setting one alone leaves a
-- provider that looks enabled and never routes.
UPDATE ad_providers
SET enabled = TRUE,
    health_status = 'HEALTHY',
    traffic_weight = 100,
    -- The direct-page banner may fill after the old 1.2-second deadline,
    -- especially when several zones queue on one page.
    request_timeout_ms = 5000,
    updated_at = now()
WHERE code = 'adsterra'
  AND public_config ? 'zones';

COMMIT;

\echo 'Adsterra routing after this run:'
SELECT code, enabled, health_status, traffic_weight,
       jsonb_object_keys(public_config -> 'zones') AS slot
FROM ad_providers
WHERE code = 'adsterra';
