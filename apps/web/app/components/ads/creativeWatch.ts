/**
 * What actually landed in an ad container, and whether anything did.
 *
 * Two questions that look separate and are not:
 *
 * 1. Spec section 4 requires us to collapse a container the provider filled
 *    with nothing: "Confirm whether they collapse unfilled in-content units. If
 *    not, SokaLens must do it: a MutationObserver on the container, collapsing
 *    it if there's no iframe after 3s." Mediavine and Adsterra's native widget
 *    both write into a container we rendered and neither hands back a fill
 *    signal, so the DOM is the only evidence there is.
 *
 * 2. Spec section 6 requires a NATIVE unit to be framed and labelled
 *    differently from a display one -- `--surface` instead of `--bg`,
 *    "Sponsored" instead of "Advertisement", a tile grid driven by container
 *    width. The difference between the two, in the DOM, is an iframe element versus
 *    inline markup.
 *
 * One observation answers both, which is the point of putting them together: a
 * slot cannot end up labelled "Sponsored" while being treated as display, and
 * it cannot be declared empty while holding a native widget that simply has no
 * iframe in it.
 */

export type AdCreativeKind = "unknown" | "display" | "native" | "empty";

/** The height transition spec section 4 gives the collapsing top slot. */
export const COLLAPSE_TRANSITION_MS = 150;

/** Spec section 4: "collapsing it if there's no iframe after 3s". */
export const EMPTY_CREATIVE_GRACE_MS = 3_000;

/**
 * Reads a container once.
 *
 * `unknown` rather than `empty` while the container is bare, because "bare" and
 * "bare for three seconds" are different facts and only the second one justifies
 * removing a slot the provider may still be filling.
 *
 * A native widget is inline DOM with layout. Both halves matter: a provider that
 * has appended its wrapper but no creative leaves element children with zero
 * height, and treating that as a fill is how the live Best Bets page ended up
 * with a labelled box around nothing.
 */
export function classifyCreative(node: Element): AdCreativeKind {
  if (node.querySelector("iframe")) return "display";
  for (const child of node.querySelectorAll("*")) {
    if (child.tagName === "SCRIPT" || child.tagName === "STYLE") continue;
    const box = child.getBoundingClientRect();
    if (box.height > 0 && box.width > 0) return "native";
  }
  return "unknown";
}

/**
 * Watches a container and reports every change of kind, settling on `empty`
 * once the grace period has passed with nothing in it.
 *
 * Returns a teardown. The callback is only invoked when the reading changes, so
 * a provider mutating its own subtree every frame cannot drive a render loop.
 */
export function observeCreative(
  node: Element,
  onChange: (kind: AdCreativeKind) => void,
): () => void {
  let last: AdCreativeKind = "unknown";
  const publish = (kind: AdCreativeKind) => {
    if (kind === last) return;
    last = kind;
    onChange(kind);
  };

  const read = () => publish(classifyCreative(node));
  read();

  if (typeof MutationObserver === "undefined") return () => undefined;
  const observer = new MutationObserver(read);
  observer.observe(node, { childList: true, subtree: true });

  const grace = setTimeout(() => {
    if (classifyCreative(node) === "unknown") publish("empty");
  }, EMPTY_CREATIVE_GRACE_MS);

  return () => {
    observer.disconnect();
    clearTimeout(grace);
  };
}
