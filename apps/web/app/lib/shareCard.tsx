import { ImageResponse } from "next/og";

import { imageSrc } from "./imageSrc";

/**
 * The card people see when a SokaLens link is pasted into a chat.
 *
 * Every link on the property shared the same static picture, and that picture
 * said "SokaStats" -- a name the site does not use. So a fixture link, the most
 * shared thing here, announced the wrong brand and told the reader nothing
 * about the match. The share card is often the only impression a link makes:
 * in a group chat it is the whole advert.
 *
 * These are generated per page instead, through Next's `opengraph-image`
 * convention, which also wires `og:image` and `twitter:image` with absolute
 * URLs. That matters because the previous metadata did not: the fixture page
 * declared its own `openGraph` block without `images`, which replaced the
 * layout's rather than merging with it, so the card fell back to the layout's
 * `twitter:image` and its site-level title. A link to Penrhyncoch vs Bangor
 * City rendered as "SokaLens - Football predictions built on data".
 *
 * Crests are fetched server-side and rasterised into the PNG, so the image the
 * reader sees is served from sokalens.com and no upstream host appears anywhere
 * in the markup.
 */

export const SHARE_CARD_SIZE = { width: 1200, height: 630 };
export const SHARE_CARD_CONTENT_TYPE = "image/png";

const INK = "#0b1914";
const GRAPHITE = "#25352f";
const PITCH = "#159b67";
const LIME = "#b8f171";
const CHALK = "#f6f9f7";
const MUTED = "#8ba093";

function Wordmark() {
  return (
    <div style={{ display: "flex", alignItems: "center", gap: 14, fontSize: 40, fontWeight: 700 }}>
      <div
        style={{
          display: "flex",
          width: 14,
          height: 44,
          borderRadius: 8,
          background: `linear-gradient(180deg, ${LIME} 0%, ${PITCH} 100%)`,
        }}
      />
      {/* One word, two colours. The halves sit in their own flex row with no
          gap, because the gap on the parent is there to separate the bar from
          the name and would otherwise split the name in half. */}
      <div style={{ display: "flex", gap: 0 }}>
        <div style={{ display: "flex", color: CHALK }}>Soka</div>
        <div style={{ display: "flex", color: PITCH }}>Lens</div>
      </div>
    </div>
  );
}

/**
 * A crest, or the team's initials when there is no usable logo.
 *
 * `imageSrc` is the same guard the pages use: the upstream sends the literal
 * string "null" often enough that an unguarded value once produced thousands of
 * 404s a day. Here a bad URL would fail the whole image render, not one crest.
 */
function Crest({ logo, name, size = 148 }: { logo?: string | null; name: string; size?: number }) {
  const src = imageSrc(logo);
  const initials = name
    .split(/\s+/)
    .filter(Boolean)
    .slice(0, 3)
    .map((word) => word[0])
    .join("")
    .toUpperCase();

  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        justifyContent: "center",
        width: size,
        height: size,
        borderRadius: size / 2,
        background: "rgba(255,255,255,0.06)",
        border: "1px solid rgba(255,255,255,0.14)",
      }}
    >
      {src ? (
        // eslint-disable-next-line @next/next/no-img-element
        <img alt="" src={src} width={size - 40} height={size - 40} style={{ objectFit: "contain" }} />
      ) : (
        <div style={{ display: "flex", color: CHALK, fontSize: size / 3, fontWeight: 700 }}>{initials || "FC"}</div>
      )}
    </div>
  );
}

function Shell({ eyebrow, children, footnote }: { eyebrow?: string; children: React.ReactNode; footnote?: string }) {
  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        width: "100%",
        height: "100%",
        padding: 64,
        background: `linear-gradient(135deg, ${INK} 0%, ${GRAPHITE} 100%)`,
        fontFamily: "sans-serif",
      }}
    >
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <Wordmark />
        {eyebrow ? (
          <div
            style={{
              display: "flex",
              color: LIME,
              fontSize: 24,
              letterSpacing: 2,
              textTransform: "uppercase",
              maxWidth: 560,
              overflow: "hidden",
            }}
          >
            {eyebrow}
          </div>
        ) : null}
      </div>

      <div style={{ display: "flex", flex: 1, flexDirection: "column", justifyContent: "center" }}>{children}</div>

      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", color: MUTED, fontSize: 22 }}>
        <div style={{ display: "flex" }}>{footnote || "Data-led football predictions"}</div>
        {/* The same treatment the site footer carries, for the same reason. */}
        <div style={{ display: "flex" }}>18+ · sokalens.com</div>
      </div>
    </div>
  );
}

/** Two teams, their crests, and the published call if there is one. */
export function fixtureShareCard({
  homeTeam,
  awayTeam,
  homeTeamLogo,
  awayTeamLogo,
  leagueName,
  kickoffLabel,
  prediction,
}: {
  homeTeam: string;
  awayTeam: string;
  homeTeamLogo?: string | null;
  awayTeamLogo?: string | null;
  leagueName?: string | null;
  kickoffLabel?: string | null;
  prediction?: string | null;
}) {
  return new ImageResponse(
    (
      <Shell eyebrow={leagueName || "Football"} footnote={kickoffLabel || "Kickoff to be confirmed"}>
        <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 32 }}>
          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 20, width: 360 }}>
            <Crest logo={homeTeamLogo} name={homeTeam} />
            <div style={{ display: "flex", color: CHALK, fontSize: 38, fontWeight: 700, textAlign: "center" }}>
              {homeTeam}
            </div>
          </div>

          <div style={{ display: "flex", color: MUTED, fontSize: 34, fontWeight: 700 }}>vs</div>

          <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 20, width: 360 }}>
            <Crest logo={awayTeamLogo} name={awayTeam} />
            <div style={{ display: "flex", color: CHALK, fontSize: 38, fontWeight: 700, textAlign: "center" }}>
              {awayTeam}
            </div>
          </div>
        </div>

        {prediction ? (
          <div
            style={{
              display: "flex",
              alignSelf: "center",
              marginTop: 36,
              padding: "12px 28px",
              borderRadius: 999,
              background: "rgba(184,241,113,0.12)",
              border: `1px solid ${PITCH}`,
              color: LIME,
              fontSize: 28,
            }}
          >
            {prediction}
          </div>
        ) : null}
      </Shell>
    ),
    SHARE_CARD_SIZE,
  );
}

/** One badge, one name -- a team or a competition. */
export function entityShareCard({
  name,
  logo,
  eyebrow,
  footnote,
}: {
  name: string;
  logo?: string | null;
  eyebrow?: string | null;
  footnote?: string | null;
}) {
  return new ImageResponse(
    (
      <Shell eyebrow={eyebrow || undefined} footnote={footnote || undefined}>
        <div style={{ display: "flex", alignItems: "center", gap: 44 }}>
          <Crest logo={logo} name={name} size={188} />
          <div style={{ display: "flex", color: CHALK, fontSize: 62, fontWeight: 700, maxWidth: 700 }}>{name}</div>
        </div>
      </Shell>
    ),
    SHARE_CARD_SIZE,
  );
}

/** The fallback card, for pages that are about the site rather than an entity. */
export function brandShareCard(headline: string, footnote?: string) {
  return new ImageResponse(
    (
      <Shell footnote={footnote}>
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <div style={{ display: "flex", color: CHALK, fontSize: 78, fontWeight: 700 }}>See the match.</div>
          <div style={{ display: "flex", color: LIME, fontSize: 78, fontWeight: 700 }}>Read the signal.</div>
          <div style={{ display: "flex", color: MUTED, fontSize: 30, marginTop: 18, maxWidth: 900 }}>{headline}</div>
        </div>
      </Shell>
    ),
    SHARE_CARD_SIZE,
  );
}

/**
 * A betting slip: a builder ticket, or an ACCA bundle.
 *
 * Slips are shared more deliberately than anything else here -- someone pastes
 * their ticket into a group to argue about it -- and they were rendering the
 * site's generic card, which says nothing about the slip.
 *
 * For the builder the legs come out of the share URL itself, so the card costs
 * no upstream calls. Only the first few are drawn: the card is 630px tall and a
 * twelve-leg slip rendered in full is unreadable at the thumbnail size most
 * people actually see.
 */
export function slipShareCard({
  heading,
  legs,
  footnote,
  odds,
}: {
  heading: string;
  legs: string[];
  footnote?: string | null;
  odds?: string | null;
}) {
  const shown = legs.slice(0, 5);
  const remaining = legs.length - shown.length;

  return new ImageResponse(
    (
      <Shell
        eyebrow={odds ? `${odds} fair odds` : undefined}
        footnote={footnote || "Build one ticket across every published market"}
      >
        <div style={{ display: "flex", flexDirection: "column", gap: 22 }}>
          <div style={{ display: "flex", color: CHALK, fontSize: 56, fontWeight: 700 }}>{heading}</div>

          <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
            {shown.map((leg, index) => (
              <div key={index} style={{ display: "flex", alignItems: "center", gap: 16 }}>
                <div style={{ display: "flex", width: 10, height: 10, borderRadius: 5, background: LIME }} />
                <div style={{ display: "flex", color: CHALK, fontSize: 28 }}>{leg}</div>
              </div>
            ))}
            {remaining > 0 ? (
              <div style={{ display: "flex", color: MUTED, fontSize: 26, marginLeft: 26 }}>
                +{remaining} more {remaining === 1 ? "leg" : "legs"}
              </div>
            ) : null}
          </div>
        </div>
      </Shell>
    ),
    SHARE_CARD_SIZE,
  );
}
