"use client";

import { useEffect, useState } from "react";

import { VIEWER_TIME_ZONE, formatInstant } from "../lib/format";

/**
 * A kickoff time in the reader's own timezone, with no flash of UTC first.
 *
 * Times were formatted in Africa/Nairobi for every visitor, which is simply
 * the wrong time for most of the audience and advertises where the site is
 * operated from. It cannot be fixed per request, because the HTML is cached at
 * the edge: one visitor's zone would be served to everyone behind that cache
 * entry.
 *
 * So the server renders UTC -- accurate, cacheable, identical for everyone --
 * and the browser corrects it in two passes:
 *
 *   1. An inline script in the document (see `localTimeScript`) rewrites every
 *      `time` element before the first paint, so the reader never sees UTC. It
 *      sweeps once and then watches for nodes that stream in after it runs,
 *      because the layout's shell is flushed before the page's own content and
 *      a single sweep therefore finds nothing on most pages.
 *   2. The effect below covers everything the inline pass cannot: client-side
 *      navigations, and any node React re-renders afterwards. It asks for
 *      `VIEWER_TIME_ZONE` explicitly -- passing `undefined` silently meant UTC,
 *      and left every row the script missed reading UTC on a reader's screen.
 *
 * The text is written with `dangerouslySetInnerHTML` rather than as a child,
 * and that is the whole trick. Pass 1 mutates the DOM before React hydrates,
 * so the server HTML and the DOM legitimately disagree by the time hydration
 * runs. `suppressHydrationWarning` alone was not enough: React still reported
 * "Minified React error #418" on every page carrying a kickoff and threw the
 * server-rendered tree away to re-render the whole page on the client, which
 * is a bad trade for a page whose Core Web Vitals matter. React does not diff
 * the children of an element it does not own, so this makes the mutation
 * invisible to hydration instead of merely quiet. Both are kept: the attribute
 * still covers the element's own attributes.
 *
 * `/about`, which renders no times, was clean while `/best-bets`, which
 * renders one, was not -- that is how this was isolated, and it is the check
 * to repeat if the error ever comes back.
 *
 * `dateTime` always carries the unambiguous instant, so assistive technology
 * and anything parsing the markup get the true value no matter which pass has
 * run.
 */
type LocalTimeProps = {
  /** ISO-8601 instant. */
  value: string;
  /** Omit the date and show only the clock time. */
  timeOnly?: boolean;
  /** Rendered when the instant cannot be parsed. */
  fallback?: string;
};

export const LOCAL_TIME_ATTRIBUTE = "data-local-time";

// Both passes go through the one formatter in lib/format.ts. A second copy of
// these options lived here and drifted from it, which is the kind of thing
// that ends with two different times on one page.
//
// `timeZoneName` is deliberately absent, here and in the inline script below:
// Intl.DateTimeFormat throws "TypeError: Invalid option : option" when it meets
// dateStyle or timeStyle, which took every page that renders a kickoff time
// down in production. The header names the reader's zone instead.
function format(value: Date, timeOnly: boolean, timeZone: string | typeof VIEWER_TIME_ZONE) {
  return formatInstant(value, { timeOnly, timeZone }) ?? "";
}

export function LocalTime({ value, timeOnly = false, fallback = "Kickoff TBC" }: LocalTimeProps) {
  const parsed = new Date(value);
  const valid = !Number.isNaN(parsed.getTime());

  // Initial state matches the server string on purpose: resolving the reader's
  // zone during render rather than in an effect is a hydration mismatch.
  const [label, setLabel] = useState(() => (valid ? format(parsed, timeOnly, "UTC") : fallback));

  useEffect(() => {
    if (!valid) return;
    setLabel(format(parsed, timeOnly, VIEWER_TIME_ZONE));
  }, [parsed, timeOnly, valid]);

  if (!valid) return <span>{fallback}</span>;
  return (
    <time
      dangerouslySetInnerHTML={{ __html: escapeText(label) }}
      dateTime={parsed.toISOString()}
      data-local-time=""
      data-time-only={timeOnly ? "1" : "0"}
      suppressHydrationWarning
    />
  );
}

/**
 * The label is `Intl.DateTimeFormat` output -- digits, spaces, a comma and a
 * meridiem -- and never contains user input. It is escaped anyway, because the
 * cost is nothing and the next person to widen this component should not have
 * to notice that it matters.
 */
function escapeText(value: string) {
  return value.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * The pre-paint pass, inlined into the document by the layout.
 *
 * A single sweep at the end of the layout is not enough, and measuring the
 * shipped HTML is what showed it: the script appeared at byte 13,543 and the
 * first `time` element at byte 22,556. The layout's shell flushes before the
 * page's own content streams in, so the sweep ran against a document that did
 * not contain a single kickoff yet, found nothing, and left every streamed row
 * showing UTC.
 *
 * So it sweeps and then observes. A MutationObserver fires on insertion,
 * before the browser paints the inserted nodes, which is what keeps the flash
 * gone for streamed content too. It stops at `load`, after which React's
 * effect owns every subsequent change.
 *
 * Deliberately dependency-free and wrapped in try/catch: a browser without a
 * resolvable timezone, or with Intl unavailable, must leave the UTC text
 * standing rather than break the page. Minified by hand because it ships in
 * every response.
 */
export const localTimeScript =
  "(function(){try{var z=Intl.DateTimeFormat().resolvedOptions().timeZone;if(!z)return;" +
  "function f(e){try{var d=new Date(e.getAttribute('datetime'));if(isNaN(d.getTime()))return;" +
  "var o={timeStyle:'short'};if(e.getAttribute('data-time-only')!=='1'){o.dateStyle='medium';}" +
  "var s=new Intl.DateTimeFormat('en',o).format(d);if(e.textContent!==s){e.textContent=s;}}catch(x){}}" +
  "function sweep(r){if(!r||!r.querySelectorAll)return;" +
  "if(r.matches&&r.matches('time[data-local-time]'))f(r);" +
  "var n=r.querySelectorAll('time[data-local-time]');for(var i=0;i<n.length;i++)f(n[i]);}" +
  "sweep(document);" +
  "if(typeof MutationObserver==='function'){var ob=new MutationObserver(function(ms){" +
  "for(var i=0;i<ms.length;i++){var a=ms[i].addedNodes;for(var j=0;j<a.length;j++){if(a[j].nodeType===1)sweep(a[j]);}}});" +
  "ob.observe(document.documentElement,{childList:true,subtree:true});" +
  "addEventListener('load',function(){ob.disconnect();});}}catch(e){}})();";
