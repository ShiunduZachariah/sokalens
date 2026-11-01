# SokaLens designs: HTML / CSS / JS export

Every page from the SokaLens design canvas (Version 31, 15 Oct 2026) as plain files that open in any browser. You don't need a build step or a server; double-click `index.html`.

## What's inside

```
index.html          page directory: every page × desktop / tablet / mobile
pages/              109 pages, one file per page per breakpoint
css/tokens.css      the colour tokens, dark + light (the only colours the pages use)
css/base.css        resets, theme-icon swap, live pulse, ad-collapse rules
js/app.js           theme toggle, ?ads=empty, ad close, tab/mode switching, favourite hearts
```

**File names:** `today.html` is the desktop page (1440), `today-tablet.html` is tablet (834) and `today-mobile.html` is mobile (390).

## Pages

| Group | Files |
|---|---|
| Home | `home` |
| Prediction lists | `today`, `tomorrow`, `weekend`, `over-2-5-goals`, `btts`, `match-result` |
| Best Bets | `best-bets`, `best-bets-locked` (signed out) |
| Leagues & teams | `leagues`, `league-argentina`, `league-premier-league`, `team` |
| Performance | `performance` |
| ACCAs | `accas`, `accas-match-result`, `accas-over-2-5`, `accas-btts`, `accas-total-corners`, `accas-total-cards`, `acca-detail`, `acca-detail-live`, `acca-detail-settled` |
| Builder | `builder` (pick), `builder-generate` |
| In-play | `in-play` |
| Match | `match-live`, `match-finished`, `match-prematch`, each with `-inplay` and `-stats` tab variants; `match` / `match-mobile` = pre-match analysis page |
| Design reference | `components-and-ads`: team crest, favourite button, in-feed native ads, featured ACCA grid |

## Themes

- `css/tokens.css` defines every colour twice, under `[data-theme="dark"]` and `[data-theme="light"]`. That includes `--fav`, the favourite heart colour.
- The pages use only `var(--…)`, so one HTML file serves both themes.
- The sun/moon button in each header switches the theme and remembers it (`localStorage: sl-theme`).
- `?theme=light` on any URL forces light mode.

## Team crests

- The coloured shields with initials mark where each team's real logo goes. They are also the fallback when a logo is missing.
- Home and away are stacked on two lines in prediction rows, each with its crest.
- Specs: `components-and-ads.html`, and `claude/sokalens-ui-review-2026-10-15.md` in the Sports App project.

## Ads

- Ad slots are marked `data-ad-slot="top | in-feed | rail | adhesion"` and show labelled placeholders at their real sizes.
- Add `?ads=empty` to any URL to see the unfilled state: every slot collapses and nothing is left behind.
- The query string carries over as you click between pages.
- The rules for native in-feed widgets are on `components-and-ads.html` and in the ad placement spec (§6).

## What works and what's static

**Works:**
- All navigation between pages: header, footer, chip rows, breadcrumbs, every row and card.
- The theme toggle.
- Closing the ad bar.
- The match tabs (Summary / In-play predictions / Stats), which open the matching file.
- The Builder Pick / Generate switch.
- Favourite hearts. They toggle on the page only; nothing is saved.

**Static:** these show their default state and do nothing when clicked:
- filters and search
- odds-format buttons
- "Load more"
- chart event selection
- the Builder ticket

The canvas design has the working versions. The data on every page is the sample data from the design.

## For the developer building the real site

- **Styles:** the markup carries inline styles exactly as designed. Treat them as the visual spec, not production CSS.
- **Components and data:** rebuild with the site's own components. Map tokens 1:1 from `css/tokens.css`.
- **Specs:** in the Sports App project:
  - `claude/sokalens-redesign-ui-handoff.md`
  - `claude/sokalens-ui-review-2026-10-15.md` (defect log UI-01 to UI-09)
  - `claude/sokalens-ad-placement-spec.md`
- **Icons:** the match-event icon set is shipped separately (`sokalens-icons.zip`).
- **Fonts:** Archivo and IBM Plex Mono, loaded from Google Fonts.
