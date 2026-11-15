import { ImageResponse } from "next/og";

import type { CampaignCard, CampaignItem } from "./campaignToken";
import { CHALK, Crest, GRAPHITE, INK, LIME, MUTED, PITCH, Wordmark } from "./shareCard";

/**
 * The images the marketing routine posts: the morning picks, the evening
 * results, a goal with live win chances, the weekly record, a stat, and a
 * "before kick-off" story.
 *
 * Two formats. A square for Telegram, Facebook and WhatsApp chats, where the
 * image sits in a feed; a 9:16 story for WhatsApp Status, TikTok, Reels and
 * Shorts, which are watched full-screen on a phone.
 *
 * Every card carries "18+" and "not a bookmaker" in its footer, because a
 * forwarded image travels without its caption.
 */

const LOSS = "#f2a29a";
const SIZES = {
  square: { width: 1080, height: 1080 },
  story: { width: 1080, height: 1920 },
} as const;

type Scale = { story: boolean; s: (value: number) => number };

function scale(card: CampaignCard): Scale {
  const story = card.format === "story";
  return { story, s: (value: number) => Math.round(value * (story ? 1.25 : 1)) };
}

function Frame({ card, children }: { card: CampaignCard; children: React.ReactNode }) {
  const { story, s } = scale(card);
  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        width: "100%",
        height: "100%",
        padding: story ? "120px 72px" : 64,
        background: `linear-gradient(160deg, ${INK} 0%, ${GRAPHITE} 100%)`,
        fontFamily: "sans-serif",
        color: CHALK,
      }}
    >
      <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between" }}>
        <Wordmark />
        {card.eyebrow ? (
          <div style={{ display: "flex", color: LIME, fontSize: s(26), letterSpacing: 2, textTransform: "uppercase", maxWidth: 640 }}>
            {card.eyebrow}
          </div>
        ) : null}
      </div>

      <div style={{ display: "flex", flex: 1, flexDirection: "column", justifyContent: "center", gap: s(28) }}>{children}</div>

      <div style={{ display: "flex", flexDirection: "column", gap: 10, color: MUTED, fontSize: s(24) }}>
        {card.note ? <div style={{ display: "flex", color: CHALK }}>{card.note}</div> : null}
        <div style={{ display: "flex", flexDirection: story ? "column" : "row", gap: 10, justifyContent: "space-between" }}>
          <div style={{ display: "flex" }}>sokalens.com</div>
          <div style={{ display: "flex" }}>18+ · Analysis, not betting advice · Not a bookmaker</div>
        </div>
      </div>
    </div>
  );
}

function Pill({ children, color = LIME, size }: { children: React.ReactNode; color?: string; size: number }) {
  return (
    <div
      style={{
        display: "flex",
        alignSelf: "flex-start",
        padding: "8px 22px",
        borderRadius: 999,
        background: "rgba(184,241,113,0.10)",
        border: `2px solid ${color}`,
        color,
        fontSize: size,
        fontWeight: 700,
      }}
    >
      {children}
    </div>
  );
}

function resultColor(result: CampaignItem["result"]): string {
  if (result === "won") return LIME;
  if (result === "lost") return LOSS;
  return MUTED;
}

function PickRow({ item, card, showResult }: { item: CampaignItem; card: CampaignCard; showResult: boolean }) {
  const { s } = scale(card);
  const many = card.items.length > 3;
  const crest = s(many ? 56 : 72);
  return (
    <div
      style={{
        display: "flex",
        alignItems: "center",
        gap: s(24),
        padding: `${s(many ? 16 : 22)}px ${s(24)}px`,
        borderRadius: 24,
        background: "rgba(255,255,255,0.05)",
        border: `1px solid ${showResult ? resultColor(item.result) : "rgba(255,255,255,0.12)"}`,
      }}
    >
      <div style={{ display: "flex", gap: 8 }}>
        <Crest logo={item.homeLogo} name={item.home} size={crest} />
        <Crest logo={item.awayLogo} name={item.away} size={crest} />
      </div>
      <div style={{ display: "flex", flexDirection: "column", flex: 1, gap: 6 }}>
        <div style={{ display: "flex", color: MUTED, fontSize: s(22) }}>
          {[item.league, showResult ? item.score && `FT ${item.score}` : item.kickoff].filter(Boolean).join(" · ")}
        </div>
        <div style={{ display: "flex", fontSize: s(many ? 30 : 34), fontWeight: 700 }}>
          {item.home} vs {item.away}
        </div>
        <div style={{ display: "flex", color: LIME, fontSize: s(28) }}>{item.pick}</div>
      </div>
      {showResult ? (
        <div style={{ display: "flex", color: resultColor(item.result), fontSize: s(30), fontWeight: 700 }}>
          {(item.result || "pending").toUpperCase()}
        </div>
      ) : item.confidence ? (
        <div style={{ display: "flex", flexDirection: "column", alignItems: "flex-end" }}>
          <div style={{ display: "flex", fontSize: s(40), fontWeight: 700 }}>{item.confidence}%</div>
          <div style={{ display: "flex", color: MUTED, fontSize: s(20) }}>confidence</div>
        </div>
      ) : null}
    </div>
  );
}

function Teams({ card }: { card: CampaignCard }) {
  const { s } = scale(card);
  const match = card.match;
  if (!match) return null;
  return (
    <div style={{ display: "flex", alignItems: "center", justifyContent: "space-between", gap: 24 }}>
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 16, width: 360 }}>
        <Crest logo={match.homeLogo} name={match.home} size={s(150)} />
        <div style={{ display: "flex", fontSize: s(38), fontWeight: 700, textAlign: "center" }}>{match.home}</div>
      </div>
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 8 }}>
        <div style={{ display: "flex", fontSize: s(match.score ? 110 : 40), fontWeight: 700, color: match.score ? CHALK : MUTED }}>
          {match.score || "vs"}
        </div>
        {match.minute ? <div style={{ display: "flex", color: LIME, fontSize: s(32) }}>{match.minute}</div> : null}
      </div>
      <div style={{ display: "flex", flexDirection: "column", alignItems: "center", gap: 16, width: 360 }}>
        <Crest logo={match.awayLogo} name={match.away} size={s(150)} />
        <div style={{ display: "flex", fontSize: s(38), fontWeight: 700, textAlign: "center" }}>{match.away}</div>
      </div>
    </div>
  );
}

function Chances({ card }: { card: CampaignCard }) {
  const { s } = scale(card);
  const probs = card.probs;
  const match = card.match;
  if (!probs || !match) return null;
  const total = probs.home + probs.draw + probs.away || 1;
  const parts = [
    { label: match.home, value: probs.home, color: LIME },
    { label: "Draw", value: probs.draw, color: MUTED },
    { label: match.away, value: probs.away, color: PITCH },
  ];
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
      <div style={{ display: "flex", color: MUTED, fontSize: s(24), textTransform: "uppercase", letterSpacing: 2 }}>Live win chances</div>
      <div style={{ display: "flex", height: s(28), borderRadius: 14, overflow: "hidden" }}>
        {parts.map((part) => (
          <div key={part.label} style={{ display: "flex", width: `${(part.value / total) * 100}%`, background: part.color }} />
        ))}
      </div>
      <div style={{ display: "flex", justifyContent: "space-between", fontSize: s(30) }}>
        {parts.map((part) => (
          <div key={part.label} style={{ display: "flex", gap: 10 }}>
            <div style={{ display: "flex", color: part.color, fontWeight: 700 }}>{Math.round(part.value / 100)}%</div>
            <div style={{ display: "flex" }}>{part.label}</div>
          </div>
        ))}
      </div>
    </div>
  );
}

function Lines({ card }: { card: CampaignCard }) {
  const { story, s } = scale(card);
  // Not scaled up on a story: a story is narrower for its height, and a line
  // such as "Last 5 meetings: Arsenal 3, draws 1, Chelsea 1" must not wrap.
  const size = story ? 36 : 34;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: s(18) }}>
      {card.lines.map((line, index) => (
        <div key={index} style={{ display: "flex", alignItems: "flex-start", gap: 20 }}>
          <div style={{ display: "flex", width: 14, height: 14, borderRadius: 7, background: LIME, marginTop: size * 0.45 }} />
          <div style={{ display: "flex", fontSize: size, maxWidth: 880 }}>{line}</div>
        </div>
      ))}
    </div>
  );
}

function Body({ card }: { card: CampaignCard }) {
  const { s } = scale(card);
  switch (card.kind) {
    case "picks":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: s(18) }}>
          {card.items.map((item, index) => (
            <PickRow key={index} item={item} card={card} showResult={false} />
          ))}
        </div>
      );
    case "results":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: s(18) }}>
          <div style={{ display: "flex", fontSize: s(72), fontWeight: 700 }}>{card.headline}</div>
          {card.items.map((item, index) => (
            <PickRow key={index} item={item} card={card} showResult />
          ))}
        </div>
      );
    case "goal":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: s(40) }}>
          {card.match?.league ? <div style={{ display: "flex", color: MUTED, fontSize: s(28) }}>{card.match.league}</div> : null}
          <Teams card={card} />
          {card.match?.event ? (
            <div style={{ display: "flex", justifyContent: "center", fontSize: s(34) }}>Goal: {card.match.event}</div>
          ) : null}
          <Chances card={card} />
        </div>
      );
    case "preview":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: s(44) }}>
          {card.match ? (
            <div style={{ display: "flex", color: MUTED, fontSize: s(28) }}>
              {[card.match.league, card.match.kickoff].filter(Boolean).join(" · ")}
            </div>
          ) : null}
          <Teams card={card} />
          <Lines card={card} />
          {card.match?.pick ? <Pill size={s(34)}>Our pick: {card.match.pick}</Pill> : null}
        </div>
      );
    case "stat":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: 20 }}>
          <div style={{ display: "flex", color: LIME, fontSize: s(220), fontWeight: 700, lineHeight: 1 }}>{card.headline}</div>
          <div style={{ display: "flex", fontSize: s(54), fontWeight: 700, maxWidth: 900 }}>{card.body}</div>
        </div>
      );
    case "record":
      return (
        <div style={{ display: "flex", flexDirection: "column", gap: s(28) }}>
          <div style={{ display: "flex", fontSize: s(96), fontWeight: 700 }}>{card.headline}</div>
          {card.body ? <div style={{ display: "flex", color: LIME, fontSize: s(40) }}>{card.body}</div> : null}
          <Lines card={card} />
        </div>
      );
  }
}

/** Renders a verified campaign card. */
export function campaignCard(card: CampaignCard, headers: Record<string, string> = {}) {
  return new ImageResponse(
    (
      <Frame card={card}>
        <Body card={card} />
      </Frame>
    ),
    { ...SIZES[card.format], headers },
  );
}
