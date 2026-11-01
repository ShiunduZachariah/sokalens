# ADR 0006: Public locale routing and preference

- Status: accepted
- Date: 2026-08-26

## Context

SokaLens needs indexable public content in English, Spanish, French, Portuguese,
Arabic, German, and Italian without creating separate sports entities or separate
prediction calculations. Country detection is useful as a hint, but it cannot
override a person's explicit choice. The administrator application is an
operations surface and is outside this localization rollout. Flutter product work
is deferred; only its localization boundary is being preserved for later.

## Decision

- Supported locale codes are `en`, `es`, `fr`, `pt`, `ar`, `de`, and `it`.
  English is the final fallback and the registry must remain extensible.
- Public localized URLs use a stable leading locale segment such as
  `/fr/predictions/today`. Localized pages self-canonicalize and publish reciprocal
  `hreflang` entries plus `x-default`.
- Existing unprefixed public routes remain compatibility entry points. Locale
  choice never depends on an IP-only forced redirect, and crawlers can request
  every locale directly.
- Resolution precedence is: explicit URL or current selection, authenticated
  `users.language`, guest cookie, browser language, country hint, then English.
- Guests persist their choice in a long-lived first-party cookie. On login or
  registration, the guest choice is saved only when the account does not already
  have an explicit supported preference.
- Official team, player, league, and competition names and their canonical slugs
  remain unchanged. Search aliases may be localized separately.
- UI copy and structured prediction explanations use translation keys/templates;
  prediction computation is not duplicated and request-time AI translation is
  prohibited.
- Arabic sets `lang="ar"` and `dir="rtl"`; shared components use logical CSS
  properties so layout direction is architectural rather than a page exception.
- Only email-verification and password-reset delivery are allowed. This ADR does
  not enable prediction, favourite, marketing, push, or other notifications.
- Locale-selection and fallback analytics reuse bounded account/growth telemetry;
  locale values are allowlisted before persistence.

## Consequences

Public routes, metadata, sitemaps, navigation, and account preference APIs must
share one locale registry. New languages extend that registry and translation
catalogues instead of adding another routing or account system. English fallback
keeps partial catalogues safe, while missing-translation tests prevent silent
coverage loss on release-critical copy.
