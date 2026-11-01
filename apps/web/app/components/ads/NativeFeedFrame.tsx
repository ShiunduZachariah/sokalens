"use client";

import { useEffect, useRef, useState, type ReactNode } from "react";
import type { AdCreativeKind } from "./creativeWatch";

/**
 * The in-feed container, and the defect it exists to prevent (UI-01, spec §6).
 *
 * On the live Best Bets page a provider's four-column "recommended content"
 * widget was rendered into a ~390px light box inside a dark page: titles broke
 * mid-word ("Thunde r vs", "Streami ng Now") and the box sat narrower than the
 * list around it. Three separate causes, all of them ours rather than the
 * provider's:
 *
 *   - the slot was centred and capped at 336px inside a wide column, so the
 *     widget laid four tiles out in a box a third of the available width;
 *   - the slot's placeholder chrome painted a hardcoded light ground;
 *   - `overflow-wrap: anywhere` on `.ad-slot__content` split words.
 *
 * So this component owns the FRAME and the layout contract, not the creative.
 * The provider still writes whatever it writes; the frame guarantees that
 * whatever it writes is the full width of the content column, on our surface
 * colour, in a grid chosen by the container's own width.
 *
 * ## Why container queries and not media queries
 *
 * Spec §6 states the layout rule against container width -- ">= 600px -> 4
 * tiles in one row, 400-599px -> 2x2, < 400px -> a list of 3" -- and the slot
 * sits in columns of very different widths on different pages: the full
 * 1200px content column on Home, an 8-of-12 column on a prediction list, a
 * narrow column inside the Builder. A viewport media query would give the
 * Builder's narrow column a four-tile row at 1440px, which is the original
 * defect with a wider box. `container-type: inline-size` here and `@container`
 * rules in globals.css are what make the rule mean what it says.
 *
 * ## The fallback
 *
 * §6: "If the widget can't lay out at the available width, or doesn't fill,
 * serve the display size from section 3 ... or collapse the slot completely."
 * The container queries cover every width down to one minimum-width tile plus
 * the frame's padding. Below that nothing in §6 can be drawn, so the frame
 * reports it and the slot collapses, which is the second branch of that
 * sentence. It is not expected to fire -- a content column narrower than 192px
 * is a layout bug elsewhere -- but a silently squeezed widget is exactly what
 * UI-01 was, so it fails loudly into a collapse instead.
 */

/** Spec §6: "Minimum tile width is 160px." Plus the frame's 16px side padding. */
export const MIN_NATIVE_FRAME_WIDTH = 160 + 16 * 2;

type NativeFeedFrameProps = {
  children: ReactNode;
  creative: AdCreativeKind;
  /** Called when the frame is too narrow for even the §6 list layout. */
  onUnfit?: () => void;
};

export function NativeFeedFrame({ children, creative, onUnfit }: NativeFeedFrameProps) {
  const frameRef = useRef<HTMLDivElement | null>(null);
  const [fit, setFit] = useState<"fits" | "narrow">("fits");
  const onUnfitRef = useRef(onUnfit);
  onUnfitRef.current = onUnfit;

  useEffect(() => {
    const frame = frameRef.current;
    if (!frame || typeof ResizeObserver === "undefined") return;
    const apply = () => {
      // 0 while the element is display:none or not yet laid out; that is not
      // "too narrow", it is "not measured", and collapsing on it would remove
      // every slot that mounts inside a hidden tab.
      const width = frame.clientWidth;
      if (width === 0) return;
      const next = width < MIN_NATIVE_FRAME_WIDTH ? "narrow" : "fits";
      setFit(next);
      if (next === "narrow") onUnfitRef.current?.();
    };
    apply();
    const observer = new ResizeObserver(apply);
    observer.observe(frame);
    return () => observer.disconnect();
  }, []);

  return (
    <div className="ad-native" data-ad-creative={creative} data-native-fit={fit} ref={frameRef}>
      {children}
    </div>
  );
}
