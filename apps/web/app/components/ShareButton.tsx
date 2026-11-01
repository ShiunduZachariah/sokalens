"use client";

import { useEffect, useId, useMemo, useRef, useState } from "react";
import { createPortal } from "react-dom";

type ShareChannel = "native" | "copy" | "whatsapp" | "facebook" | "instagram" | "x" | "telegram";
type ShareButtonProps = { compact?: boolean; entityId: string; entityType: "match" | "prediction" | "acca" | "builder"; path?: string; text: string; title: string };
type MenuPosition = { bottom?: number; left: number; top?: number };

function ShareIcon() {
  return <svg aria-hidden="true" fill="none" height="18" viewBox="0 0 24 24" width="18"><circle cx="18" cy="5" r="2.5" stroke="currentColor" strokeWidth="1.8"/><circle cx="6" cy="12" r="2.5" stroke="currentColor" strokeWidth="1.8"/><circle cx="18" cy="19" r="2.5" stroke="currentColor" strokeWidth="1.8"/><path d="m8.2 10.8 7.6-4.5M8.2 13.2l7.6 4.5" stroke="currentColor" strokeLinecap="round" strokeWidth="1.8"/></svg>;
}

function ChannelIcon({ channel }: { channel: ShareChannel }) {
  if (channel === "native") return <ShareIcon />;
  if (channel === "copy") return <svg aria-hidden="true" fill="none" viewBox="0 0 24 24"><path d="M9 8h9a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H9a2 2 0 0 1-2-2v-9a2 2 0 0 1 2-2Z" stroke="currentColor" strokeWidth="1.8"/><path d="M16 8V5a2 2 0 0 0-2-2H5a2 2 0 0 0-2 2v9a2 2 0 0 0 2 2h2" stroke="currentColor" strokeWidth="1.8"/></svg>;
  if (channel === "whatsapp") return <svg aria-hidden="true" fill="none" viewBox="0 0 24 24"><path d="M20.5 11.7a8.5 8.5 0 0 1-12.6 7.5L3.5 20.5l1.3-4.2a8.5 8.5 0 1 1 15.7-4.6Z" stroke="currentColor" strokeWidth="1.8"/><path d="M9 7.7c.3-.4.7-.3.9.1l1 2c.1.3 0 .6-.3.8l-.7.6c.7 1.4 1.8 2.5 3.2 3.2l.6-.8c.2-.3.5-.4.8-.2l2 1c.4.2.5.6.2.9-.6.8-1.4 1.2-2.4 1-3.7-.8-6.6-3.7-7.4-7.4-.2-.5.3-1.1.7-1.5" stroke="currentColor" strokeLinecap="round" strokeLinejoin="round" strokeWidth="1.5"/></svg>;
  if (channel === "facebook") return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M14.2 21v-8h2.7l.4-3.1h-3.1v-2c0-.9.3-1.5 1.6-1.5h1.7V3.6c-.8-.1-1.7-.2-2.5-.2-2.5 0-4.2 1.5-4.2 4.3v2.2H8V13h2.8v8h3.4Z" fill="currentColor"/></svg>;
  if (channel === "instagram") return <svg aria-hidden="true" fill="none" viewBox="0 0 24 24"><rect height="17" rx="4.5" stroke="currentColor" strokeWidth="1.8" width="17" x="3.5" y="3.5"/><circle cx="12" cy="12" r="3.6" stroke="currentColor" strokeWidth="1.8"/><circle cx="17.6" cy="6.7" fill="currentColor" r="1"/></svg>;
  if (channel === "x") return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="M5 4h3.8l3.9 5.2L17.2 4H19l-5.5 6.5L19.8 20H16l-4.4-5.9L6.5 20H4.7l6-7.2L5 4Zm3 1.5 8.8 13h1L9 5.5H8Z" fill="currentColor"/></svg>;
  return <svg aria-hidden="true" viewBox="0 0 24 24"><path d="m21 4-3.1 15c-.2 1-1 1.2-1.8.7l-4.7-3.5-2.3 2.2c-.2.3-.5.5-1 .5l.4-4.8L17.2 6c.4-.4-.1-.6-.6-.2L5.8 12.6l-4.6-1.5c-1-.3-1-1 .2-1.5L19.6 2.6C20.5 2.3 21.2 2.8 21 4Z" fill="currentColor"/></svg>;
}

function trackingID(value: string) {
  if (value.length <= 240 && /^[A-Za-z0-9:._~-]+$/.test(value)) return value;
  let hash = 2166136261;
  for (let index = 0; index < value.length; index += 1) { hash ^= value.charCodeAt(index); hash = Math.imul(hash, 16777619); }
  return `ref-${(hash >>> 0).toString(16)}`;
}

const channels: Array<{ channel: Exclude<ShareChannel, "native">; label: string }> = [
  { channel: "whatsapp", label: "WhatsApp" }, { channel: "facebook", label: "Facebook" },
  { channel: "instagram", label: "Instagram" }, { channel: "x", label: "X" },
  { channel: "telegram", label: "Telegram" }, { channel: "copy", label: "Copy link" },
];

export function ShareButton({ compact = false, entityId, entityType, path, text, title }: ShareButtonProps) {
  const menuId = useId();
  const [open, setOpen] = useState(false);
  const [feedback, setFeedback] = useState("");
  const [menuPosition, setMenuPosition] = useState<MenuPosition>();
  /**
   * Whether this browser can open the system share sheet.
   *
   * Read in an effect rather than during render, and that distinction is the
   * whole bug: `typeof navigator.share === "function"` is false on the server
   * and true in Chrome, so rendering the option conditionally produced
   * different markup on each side. React reported "Minified React error #418"
   * on every page carrying this button and threw the server render away to
   * re-render the whole tree on the client.
   *
   * It went unnoticed because the button is in PredictionExplorer and not in
   * BestBetExplorer, so /best-bets and the fixture pages were clean while
   * /predictions/today, /tomorrow and /weekend were not -- which reads like a
   * problem with those pages rather than with a shared control.
   *
   * Starting false matches the server, so hydration agrees; the option appears
   * a tick later on the browsers that have it.
   */
  const [canShareNatively, setCanShareNatively] = useState(false);

  useEffect(() => {
    setCanShareNatively(typeof navigator !== "undefined" && typeof navigator.share === "function");
  }, []);
  const rootRef = useRef<HTMLDivElement>(null);
  const menuRef = useRef<HTMLDivElement>(null);
  const trackedID = useMemo(() => trackingID(entityId), [entityId]);

  useEffect(() => {
    if (!open) return;
    const trigger = rootRef.current?.querySelector("button");
    if (!trigger) return;
    const rect = trigger.getBoundingClientRect();
    if (window.innerWidth <= 767) {
      setMenuPosition({ bottom: 72, left: 16 });
    } else {
    const menuWidth = 216;
    const menuHeight = canShareNatively ? 384 : 336;
    const left = Math.max(12, Math.min(rect.right - menuWidth, window.innerWidth - menuWidth - 12));
    setMenuPosition(window.innerHeight - rect.bottom >= menuHeight ? { left, top: rect.bottom + 8 } : { bottom: Math.max(12, window.innerHeight - rect.top + 8), left });
    }
    const close = (event: MouseEvent) => {
      const target = event.target as Node;
      if (!rootRef.current?.contains(target) && !menuRef.current?.contains(target)) setOpen(false);
    };
    const escape = (event: KeyboardEvent) => { if (event.key === "Escape") { setOpen(false); (trigger as HTMLButtonElement).focus(); } };
    const closeForViewportChange = () => setOpen(false);
    document.addEventListener("mousedown", close);
    document.addEventListener("keydown", escape);
    window.addEventListener("resize", closeForViewportChange);
    window.addEventListener("scroll", closeForViewportChange, true);
    return () => {
      document.removeEventListener("mousedown", close); document.removeEventListener("keydown", escape);
      window.removeEventListener("resize", closeForViewportChange); window.removeEventListener("scroll", closeForViewportChange, true);
    };
  }, [open, canShareNatively]);

  const shareURL = () => new URL(path ?? `${window.location.pathname}${window.location.search}`, window.location.origin).toString();
  const track = (channel: ShareChannel) => { void fetch("/api/account/v1/share-events", { body: JSON.stringify({ channel, entity_id: trackedID, entity_type: entityType }), cache: "no-store", credentials: "include", headers: { "Content-Type": "application/json" }, keepalive: true, method: "POST" }); };
  const copy = async (url: string) => { try { await navigator.clipboard.writeText(`${text}\n${url}`); setFeedback("Link copied"); return true; } catch { setFeedback("Copy unavailable"); return false; } };
  const choose = async (channel: ShareChannel) => {
    const url = shareURL(); setOpen(false);
    if (channel === "native" && navigator.share) { try { await navigator.share({ title, text, url }); track(channel); } catch { /* The user closed the system sheet. */ } return; }
    if (channel === "copy") { if (await copy(url)) track(channel); return; }
    track(channel);
    if (channel === "instagram") { window.open("https://www.instagram.com/", "_blank", "noopener,noreferrer"); await copy(url); setFeedback("Link copied — paste it in Instagram"); return; }
    const encodedURL = encodeURIComponent(url); const encodedText = encodeURIComponent(text);
    const targets: Partial<Record<ShareChannel, string>> = {
      whatsapp: `https://wa.me/?text=${encodeURIComponent(`${text}\n${url}`)}`,
      facebook: `https://www.facebook.com/sharer/sharer.php?u=${encodedURL}`,
      x: `https://x.com/intent/post?text=${encodedText}&url=${encodedURL}`,
      telegram: `https://t.me/share/url?url=${encodedURL}&text=${encodedText}`,
    };
    const target = targets[channel]; if (target) window.open(target, "_blank", "noopener,noreferrer");
  };

  const menu = open && menuPosition ? <div aria-label={`Share ${title}`} className="share-control__menu" id={menuId} ref={menuRef} role="menu" style={menuPosition}>
    <strong className="share-control__menu-title">Share to</strong>
    {canShareNatively ? <button className="share-control__option share-control__option--native" onClick={() => void choose("native")} role="menuitem" type="button"><ChannelIcon channel="native"/><span>Share via device</span></button> : null}
    {channels.map(({ channel, label }) => <button className={`share-control__option share-control__option--${channel}`} key={channel} onClick={() => void choose(channel)} role="menuitem" type="button"><ChannelIcon channel={channel}/><span>{label}</span></button>)}
  </div> : null;

  return <div className={`share-control${compact ? " share-control--compact" : ""}`} ref={rootRef}>
    <button aria-controls={menuId} aria-expanded={open} aria-haspopup="menu" className="share-control__toggle" onClick={() => { setFeedback(""); setOpen((current) => !current); }} type="button"><ShareIcon/><span>Share</span></button>
    {typeof document !== "undefined" && menu ? createPortal(menu, document.body) : null}
    {feedback ? <span aria-live="polite" className="share-control__feedback">{feedback}</span> : null}
  </div>;
}
