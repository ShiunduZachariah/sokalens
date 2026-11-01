# SokaLens UI refresh — design source

Source canvas: `https://claude.ai/artifact/V4JhraarCZ6fzpFkEmaYEu`
Synced from canvas version `1790926646-a6d1` on 2026-10-02.
These files are the **authoritative visual spec** for
`docs/ai/plans/2026-09-27-ui-refresh-match-and-acca.md`.

The canvas holds 172 artboards, but only 36 of the 47 files here are real
sources. The other 11 are kept deliberately: a wrapper is how you learn which
prop value produces which page (`Weekend.dc.html` is three lines and tells you
the Weekend page is `PList view="weekend"`). Everything else on the canvas is
an empty placeholder or another such wrapper (~720 bytes each) that re-renders
a source with one prop changed — so a light layout is never a separate design,
it is the same file with `theme = light`, and neither is a market hub: it is
`PList` with `view = "o25"`. That is the single most important fact about this
set.

The 2026-10-01 sync added 13 sources — the prediction-list template and the
league, team, Best Bets and performance pages — plus the 11 wrappers.

The 2026-10-02 sync changed four: **BestBets** gained an `access="locked"` state
(the sign-up gate, with the hero pick deliberately outside it), **AccaDetail**
gained `result="live"` and `result="settled"`, **LiveMatch** gained
`status="prematch"`, and **InPlay** replaced its dead rail links and added a row
with no pre-match pick, whose tracker chip is `display: none` — the same
"show nothing" the data contract requires for an absent tracker state.

| File | Page | Viewport |
| --- | --- | --- |
| `Home.dc.html` / `HomeTablet` / `HomeMobile` | Home | 1440×3496 · 834×3936 · 390×4956 |
| `Main.dc.html` / `Mobile` | Match page | 1440×2936 · 390×4076 |
| `Today.dc.html` / `TodayTablet` / `TodayMobile` | Today's predictions | 1440×3540 · 834×4140 · 390×4520 |
| `Acca.dc.html` / `AccaTablet` / `AccaMobile` | ACCA predictions (list) | 1440×4396 · 834×3636 · 390×5076 |
| `AccaDetail.dc.html` / `…Tablet` / `…Mobile` | ACCA detail (one ticket) | 1440×2416 · 834×2616 · 390×3156 |
| `Builder.dc.html` / `BuilderTablet` / `BuilderMobile` | **ACCA Builder** (new) | 1440×3740 · 834×3440 · 390×3960 |
| `InPlay.dc.html` / `InPlayTablet` / `InPlayMobile` | **In-play list** (new) | 1440×2756 · 834×2956 · 390×3636 |
| `LiveMatch.dc.html` / `…Tablet` / `…Mobile` | **Live / finished match** (new) | 1440×2620 · 834×3356 · 390×3836 |
| `PList.dc.html` / `PListTablet` / `PListMobile` | **Prediction list** — serves five pages | 1440×2880 · 834 · 390 |
| `BestBets.dc.html` / `BestBetsMobile` | **Best Bets** (new) | 1440 · 390 |
| `Leagues.dc.html` / `LeaguesMobile` | **Leagues index** (new) | 1440 · 390 |
| `League.dc.html` / `LeagueMobile` | **League hub** (new) | 1440×2840 · 390 |
| `Team.dc.html` / `TeamMobile` | **Team page** (new) | 1440 · 390 |
| `Performance.dc.html` / `PerformanceMobile` | **Performance / results** (new) | 1440 · 390 |

## States are props on one source, not separate artboards

The canvas has 76 boards but 23 sources because every variant is a thin
`<dc-import>` wrapper passing a prop:

| Wrapper suffix | Prop | Meaning |
| --- | --- | --- |
| `…Light` | `theme="light"` | the light twin of any board |
| `TodayNoFill`, `TodayMobileNoFill` | `ads="empty"` | every ad slot unfilled |
| `BuilderGenerate…` | `start-mode="generate"` | Builder opens in Generate mode |
| `LiveMatch…Inplay` | `start-tab="inplay"` | match page opens on the in-play tab |
| `LiveMatch…Finished` | status finished | the same match after full time |
| `Weekend`, `MarketO25`, `MarketBTTS`, `MarketMR` | `view="weekend"` \| `"o25"` \| `"btts"` \| `"mr"` | on `PList`; `view="tomorrow"` is its default |
| `AccaMR`, `AccaO25`, `AccaBTTS`, `AccaCorners`, `AccaCards` | `preset="mr"` … | on `Acca` — the five ACCA type pages |
| `LeagueEPL…` | `league="epl"` | `League` with one league's data |
| `BestBetsLocked…` | `access="locked"` | Best Bets behind the sign-up gate |
| `AccaDetailLive…`, `AccaDetailSettled…` | `result="live"` \| `"settled"` | an ACCA in progress, and after it settles |
| `LiveMatchPrematch…` | `status="prematch"` | the match page before kick-off |

**The five prediction-list pages are one artboard.** Tomorrow, Weekend,
Over 2.5, BTTS and Match result are `PList` with a `view` prop, which is why
`apps/web/app/components/PredictionListShell.tsx` exists: they are one React
component too, and a change to a row must land in one place.

So editing a dark source updates its light twin, its no-fill twin and its
state variants at once.

## Advertising slots (new, every template)

| Slot | Desktop | Tablet | Mobile | Where |
| --- | --- | --- | --- | --- |
| `top` | 970×90 | 728×90 | 320×50 | under the header |
| `in-feed` | 728×90 | 728×90 | 300×250 | once per page, between blocks |
| `rail` | 300×600 | — | — | desktop side columns, sticky |
| `adhesion` | — | — | 320×50 | above the mobile tab bar |

`rail` appears only on boards that have a desktop side column — of the built
pages, that is Today alone. `adhesion` is excluded from the Builder, whose own
sticky ticket owns that edge on small screens.

**The rule that matters more than the sizes:** an unfilled slot leaves nothing
behind — no border, no label, no reserved height. That is what the
"Ads unfilled (collapsed)" boards on Page 4 exist to show. The canvas notes
cite a project doc `claude/sokalens-ad-placement-spec.md`, which is not in this
repo; the table above is read off the artboards' own `data-ad-slot` and
`data-ad-size` attributes.

Breakpoints are therefore **1440 / 834 / 390**. The match page ships desktop
and mobile only; Home, ACCA list and ACCA detail have all three.

**Home sections** (`Home.dc.html`): hero → strongest signal of the day
(auto-picked as the highest-confidence prediction) → quick links → today's
predictions with a 1 · X · 2 marker → how it works → featured ACCAs (2-leg cap,
same card as the ACCA list) → disclaimer → footer.

**Today sections** (`Today.dc.html`): header with the day's totals (fixtures ·
predictions · markets) → 8-day date strip → quick filters (search, country,
market) → group-by toggle (Competition / Kick-off) → fixture rows carrying
kick-off, the match, a 1 · X · 2 marker, the pick with its market, and
confidence where published → empty state with Clear filters.

## The theme token set

Defined once in each artboard's `renderVals()` as `THEMES`, emitted as CSS
custom properties on the artboard root. Port these verbatim — they are the
design system, and dark/light parity depends on using the same key for the
same job in both.

| Token | Dark | Light |
| --- | --- | --- |
| `--bg` | `#0E1411` | `#F8F9F7` |
| `--surface` | `#131B16` | `#FFFFFF` |
| `--surface-2` (highlighted card) | `#16201A` | `#F0F7E8` |
| `--surface-3` (inset control) | `#1C2620` | `#F1F3F0` |
| `--surface-sunk` (card footer) | `#0F1712` | `#F4F6F3` |
| `--bar` | `#0B100D` | `#FFFFFF` |
| `--hatch` (empty-slot stripe) | `#18221C` | `#E6E9E4` |
| `--line` | `#243029` | `#E1E5DF` |
| `--line-soft` | `#1F2A23` | `#ECEFEA` |
| `--line-strong` | `#2E3B34` | `#D1D7CF` |
| `--line-stronger` | `#3B4741` | `#BDC4BB` |
| `--line-heavy` | `#4A5750` | `#9AA39A` |
| `--tick` (50% marker) | `#5A675F` | `#7F8A80` |
| `--deco` (pitch line-art) | `#1E2922` | `#EBEEE9` |
| `--text` | `#EEF2EC` | `#111812` |
| `--text-2` | `#DCE4DD` | `#1D271F` |
| `--text-3` | `#C9D2CB` | `#364238` |
| `--text-4` | `#B8C3BB` | `#445047` |
| `--muted` | `#A7B3AB` | `#56635A` |
| `--faint` | `#8A968E` | `#66736A` |

### Accent: fill and ink are separate tokens

The lime accent is legible on dark but fails as text on light, so the design
splits it. **Do not collapse these into one token.**

| Token | Dark | Light | Use |
| --- | --- | --- | --- |
| `--accent` | `#C8F169` | `#4C9F16` | fills, bars, chips, primary button ground |
| `--accent-ink` | `#C8F169` | `#2F6B0A` | accent-coloured **text** and underlines |
| `--away` | `#8FB8FF` | `#3D7BE0` | away-side fills |
| `--away-ink` | `#8FB8FF` | `#1F5BBF` | away-side text |
| `--on-accent` | `#0E1411` | `#0E1411` | text on an accent fill, both themes |

Accents are switchable (`#C8F169` default, plus `#5EE6C1`, `#FFB547`,
`#F2F2EC`; away `#8FB8FF`, `#FF9F7A`, `#D6B4FF`). Only the default pair has a
hand-tuned light mapping; the artboards fall back to
`color-mix(in oklab, <accent> 42%, #0E1411)` for the rest. `color-scheme` is
also set per theme.

## Fonts

The artboards load exactly:

```
https://fonts.googleapis.com/css2?family=Archivo:wdth,wght@62..125,400..900&family=IBM+Plex+Mono:wght@400;500;600&display=swap
```

Archivo is requested with the **`wdth` axis 62..125**. The design's headings
sit at `font-stretch: 62%–75%`, so the variable-width font is required — a
static Archivo cut cannot render this design.

## Reading an artboard

Each file is a Design Component (`.dc.html`), not a runnable page:

- `{{token}}` is a hole filled by `renderVals()` at the foot of the file. That
  block carries the mock data *and* the derived-figure formulas — read it for
  intended rounding, label wording, points/record computation and bar widths.
- `<sc-for list="{{items}}" as="x">` is a repeat, `<sc-if>` a branch,
  `<dc-import>` a child artboard.
- Layout is inline `style="…"` in absolute px at the artboard's fixed width.
  Treat the px as ratios to port into responsive CSS, not literals.
- All eight are `is_interactive: true`: filters, search, odds-format switching,
  load-more, leg expansion and the theme toggle genuinely work in Play.
- `@font-face` blocks were stripped here for readability; the `<link>` above is
  the real dependency.

Nothing is wired to an API. Every figure shown is derivable from data the web
app already holds — the plan's mapping tables say where from.
