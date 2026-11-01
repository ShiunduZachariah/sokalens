"use client";

import { useCallback, useEffect, useLayoutEffect, useRef, useState, type ReactNode } from "react";
import { parseAdPlacementDecision, type AdProviderAttempt } from "../lib/adDelivery";
import { createAdAttemptTimer } from "../lib/adAttemptTimer";
import { useAccount } from "./account/AccountContext";
import { NativeFeedFrame } from "./ads/NativeFeedFrame";
import { providerAdapter } from "./ads/providerRegistry";
import { COLLAPSE_TRANSITION_MS, observeCreative, type AdCreativeKind } from "./ads/creativeWatch";
import type { AdapterResult, AdFormat } from "./ads/types";
import { ADS_ENABLED } from "../lib/adsEnabled";

/**
 * Slot formats.
 *
 * The first four are the canonical placements of the 2026-09-30 design, and
 * each carries its own responsive size ladder in globals.css:
 *
 *   top       970x90 desktop · 728x90 tablet · 320x50 mobile, under the header
 *   in-feed   728x90 desktop · 300x250 mobile, once per page between blocks
 *   rail      300x600, sticky, desktop side columns only
 *   adhesion  320x50, fixed above the mobile tab bar
 *
 * The four after them are the pre-refresh names. They stay because /results,
 * /leagues and the other un-refreshed pages still use them; retiring them
 * would leave those pages with no slots at all rather than with old ones.
 */
export type { AdFormat } from "./ads/types";

type AdSlotProps = {
  children?: ReactNode;
  className?: string;
  format: AdFormat;
  label?: string;
  /**
   * Supplied, a filled slot carries a close button that calls this.
   *
   * Only the adhesion band uses it: spec section 3 gives that slot, and only
   * that slot, "320x50 in a 60px bar, with a close button". Dismissal is the
   * owner's business -- AdSlot does not know whether the band should stay gone
   * for the session or the page -- so it reports the press and nothing else.
   */
  onDismiss?: () => void;
  placement?: string;
};
type DeliveryState = "idle" | "loading" | "attempting" | "success" | "exhausted";

/**
 * The four states of the 2026-10-15 spec (section 4), derived from the richer
 * delivery machine above so there is exactly one place that decides what a
 * reader sees.
 *
 *   idle     nothing asked for yet
 *   loading  a decision or a provider attempt is in flight
 *   filled   a provider reported a creative
 *   empty    no fill, no provider, blocked, or ads not allowed for this reader
 *
 * Published as `data-ad-state` so the behaviour is observable in a test and in
 * the browser rather than inferred from class names.
 */
export type AdSlotState = "idle" | "loading" | "filled" | "empty";

function slotState(delivery: DeliveryState, resolved: boolean, allowed: boolean): AdSlotState {
  // Spec section 4: "an ad blocker or refused consent is treated the same as
  // `empty`". A reader whose entitlement says no ads is the same case.
  if (resolved && !allowed) return "empty";
  if (!resolved) return "loading";
  if (delivery === "success") return "filled";
  if (delivery === "exhausted") return "empty";
  if (delivery === "idle") return "idle";
  return "loading";
}

async function reportOutcome(attempt: AdProviderAttempt, result: AdapterResult, durationMs: number) {
  const body = result.outcome === "success"
    ? { attempt_token: attempt.attemptToken, duration_ms: durationMs, outcome: "success" }
    : { attempt_token: attempt.attemptToken, duration_ms: durationMs, failure_reason: result.failureReason, outcome: "failure" };
  try {
    await fetch("/api/ads/outcomes", {
      body: JSON.stringify(body), cache: "no-store", credentials: "include",
      headers: { "Content-Type": "application/json" }, method: "POST",
    });
  } catch {
    // Outcome telemetry must never hold a slot open or break page content.
  }
}

function EnabledAdSlot({ children, className, format, label = "Advertisement", onDismiss, placement = format }: AdSlotProps) {
  const { access, status } = useAccount();
  const [viewportEligible, setViewportEligible] = useState(format !== "rail" && format !== "adhesion");
  const [delivery, setDelivery] = useState<DeliveryState>("idle");
  const [providers, setProviders] = useState<AdProviderAttempt[]>([]);
  const [providerIndex, setProviderIndex] = useState(0);
  const [creative, setCreative] = useState<AdCreativeKind>("unknown");
  // The reserved top box has finished its collapse animation and may leave the
  // document. Nothing else uses it: every other format renders nothing at all
  // when empty, so there is no height to animate away.
  const [collapsed, setCollapsed] = useState(false);
  const contentRef = useRef<HTMLDivElement | null>(null);
  const activeToken = useRef("");
  const attemptStartedAt = useRef(0);
  const attemptTimer = useRef<ReturnType<typeof createAdAttemptTimer> | null>(null);
  const settled = useRef(false);
  const directCreative = children !== undefined && children !== null;
  const attempt = providers[providerIndex];
  // `unavailable` is resolved, not pending: an access API that cannot answer is
  // spec section 4's "blocked" case, and a slot that treated it as still
  // loading would hold its reserved height open forever.
  const resolved = status !== "loading";
  const allowed = status === "ready" && Boolean(access?.adsAllowed);
  const state = slotState(delivery, resolved, allowed);

  useEffect(() => {
    if (format !== "rail" && format !== "adhesion") {
      setViewportEligible(true);
      return;
    }
    const media = window.matchMedia(format === "rail" ? "(min-width: 1181px)" : "(max-width: 767px)");
    const update = () => setViewportEligible(media.matches);
    update();
    media.addEventListener("change", update);
    return () => media.removeEventListener("change", update);
  }, [format]);

  useEffect(() => {
    if (!viewportEligible || directCreative || status !== "ready" || !access?.adsAllowed) {
      setDelivery("idle"); setProviders([]); setProviderIndex(0);
      return;
    }
    const controller = new AbortController();
    setDelivery("loading");
    void fetch(`/api/ads/placements/${encodeURIComponent(placement)}`, {
      cache: "no-store", credentials: "include", signal: controller.signal,
    }).then(async (response) => response.ok ? parseAdPlacementDecision(await response.json()) : null)
      .then((decision) => {
        if (controller.signal.aborted) return;
        if (!decision?.allowed || decision.providers.length === 0) {
          setDelivery("exhausted"); setProviders([]); return;
        }
        setProviders(decision.providers); setProviderIndex(0); setDelivery("attempting");
      }).catch(() => { if (!controller.signal.aborted) setDelivery("exhausted"); });
    return () => controller.abort();
  }, [access?.adsAllowed, directCreative, placement, status, viewportEligible]);

  const finishAttempt = useCallback((token: string, result: AdapterResult) => {
    const current = providers[providerIndex];
    if (!current || current.attemptToken !== token || activeToken.current !== token || settled.current) return;
    settled.current = true;
    const durationMs = Math.max(0, Math.min(60_000, Math.round(performance.now() - attemptStartedAt.current)));
    void reportOutcome(current, result, durationMs);
    if (result.outcome === "success") setDelivery("success");
    else if (providerIndex + 1 < providers.length) setProviderIndex((value) => value + 1);
    else setDelivery("exhausted");
  }, [providerIndex, providers]);

  const startAttempt = useCallback((token: string) => {
    if (activeToken.current !== token || settled.current) return;
    if (attemptTimer.current?.start()) attemptStartedAt.current = performance.now();
  }, []);

  useLayoutEffect(() => {
    if (delivery !== "attempting" || !attempt) return;
    activeToken.current = attempt.attemptToken;
    attemptStartedAt.current = performance.now();
    settled.current = false;
    attemptTimer.current = createAdAttemptTimer(
      attempt.timeoutMs,
      () => finishAttempt(attempt.attemptToken, { outcome: "failure", failureReason: "TIMEOUT" }),
    );
    // Adsterra banners wait for the shared atOptions queue. Their fill budget
    // begins when the adapter actually starts the script, not while queued.
    if (attempt.adapterKey !== "adsterra") startAttempt(attempt.attemptToken);
    return () => {
      activeToken.current = "";
      attemptTimer.current?.stop();
      attemptTimer.current = null;
    };
  }, [attempt, delivery, finishAttempt, startAttempt]);

  /*
   * Spec section 4, the provider-inserts-its-own-units case.
   *
   * Mediavine (and Adsterra's native widget) write into a container we rendered
   * rather than handing back a fill signal. "Confirm whether they collapse
   * unfilled in-content units. If not, SokaLens must do it: a MutationObserver
   * on the container, collapsing it if there's no iframe after 3s."
   *
   * The same observation answers the other question this component now has to
   * ask -- display or native -- because that is the difference between an
   * an iframe element and inline DOM. One observer, two readings, so a slot cannot
   * end up labelled "Sponsored" while being treated as a display unit.
   *
   * No second outcome report: the attempt is already settled in telemetry as a
   * success and re-reporting it would double-count the attempt. This is a
   * rendering decision about a creative that arrived empty, not a new attempt.
   */
  useEffect(() => {
    const node = contentRef.current;
    if (delivery !== "success" || !node) return;
    return observeCreative(node, (kind) => {
      setCreative(kind);
      if (kind === "empty") setDelivery("exhausted");
    });
  }, [delivery]);

  /*
   * The 150ms collapse, and why only the top slot has one.
   *
   * Spec section 4 gives exactly one slot a reserved height -- `top` -- and so
   * exactly one slot has a height to animate away when nothing fills it. Every
   * other format renders no element until it is filled, so there is nothing to
   * transition and nothing to unmount. Once the animation has run the element
   * leaves the document, because acceptance criterion 1 is that an unfilled
   * slot leaves no box, label or gap behind -- a retained 0-height aside would
   * satisfy the pixels and not the criterion.
   */
  useEffect(() => {
    if (format !== "top") return;
    if (state !== "empty") { setCollapsed(false); return; }
    const timer = window.setTimeout(() => setCollapsed(true), COLLAPSE_TRANSITION_MS);
    return () => window.clearTimeout(timer);
  }, [format, state]);

  const resolvedLabel = creative === "native" ? "Sponsored" : label;
  const classes = ["ad-slot", `ad-slot--${format}`, className].filter(Boolean).join(" ");

  if (directCreative) {
    if (!viewportEligible || !resolved || !allowed) return null;
    return (
      <aside aria-label={`${label} placement`} className={classes} data-ad-format={format} data-ad-placement={placement} data-ad-policy="eligible" data-ad-state="filled">
        <span className="ad-slot__label">{label}</span><div className="ad-slot__content">{children}</div>
      </aside>
    );
  }

  /*
   * The collapse-to-nothing guard, and the one documented exception.
   *
   * Every format except `top` renders no element at all until a creative
   * exists: that is what keeps an unfilled slot from leaving a bordered box
   * behind, and it is why `rail`, `in-feed` and `adhesion` cost no CLS (spec
   * section 4, columns "Initial render" and "On empty").
   *
   * `top` is the deliberate exception and the only slot allowed to cost CLS.
   * It reserves its height from the FIRST render -- before entitlement has even
   * resolved -- so the creative lands inside a box that is already there. On no
   * fill it animates that box to nothing and then unmounts, which moves the
   * page up once. Spec section 4: "The page moves up once, which is acceptable
   * because it only happens on no-fill."
   */
  const reservingTop = format === "top" && !collapsed && state !== "filled";
  if (!reservingTop && (!viewportEligible || !resolved || !allowed)) return null;
  if (!reservingTop && ((delivery !== "attempting" && delivery !== "success") || !attempt)) return null;

  const visible = state === "filled";
  const stateClass = visible
    ? " ad-slot--ready"
    : format === "top"
      ? state === "empty" ? " ad-slot--collapsing" : " ad-slot--reserved"
      : " ad-slot--pending";
  const Adapter = attempt ? providerAdapter(attempt.adapterKey) : null;
  const body = (
    <div className="ad-slot__content" ref={contentRef}>
      {Adapter && attempt ? (
        <Adapter
          attempt={attempt}
          format={format}
          key={attempt.attemptToken}
          onResult={(result) => finishAttempt(attempt.attemptToken, result)}
          onStart={() => startAttempt(attempt.attemptToken)}
        />
      ) : null}
    </div>
  );

  return (
    <aside
      aria-hidden={visible ? undefined : true}
      aria-label={visible ? `${resolvedLabel} placement` : undefined}
      className={`${classes}${stateClass}`}
      data-ad-creative={visible ? creative : undefined}
      data-ad-format={format} data-ad-placement={placement} data-ad-policy="eligible"
      data-ad-provider={attempt?.code}
      data-ad-state={state}
    >
      {visible && onDismiss ? (
        <button aria-label="Close advertisement" className="ad-slot__close" onClick={onDismiss} type="button">
          <svg aria-hidden="true" fill="none" height="14" stroke="currentColor" strokeWidth="2" viewBox="0 0 24 24" width="14">
            <path d="M6 6l12 12M18 6L6 18" />
          </svg>
        </button>
      ) : null}
      {visible ? <span className="ad-slot__label">{resolvedLabel}</span> : null}
      {format === "in-feed" ? (
        <NativeFeedFrame creative={creative} onUnfit={() => setDelivery("exhausted")}>{body}</NativeFeedFrame>
      ) : body}
    </aside>
  );
}

/** Renders nothing while ads are switched off; see `lib/adsEnabled`. */
export function AdSlot(props: AdSlotProps) {
  return ADS_ENABLED ? <EnabledAdSlot {...props} /> : null;
}
