"use client";

import { useEffect, useState } from "react";

/**
 * Names the timezone the reader is actually seeing times in.
 *
 * The header used to assert "Timezone: EAT" to every visitor on earth. It now
 * says UTC until hydration and then the reader's own zone, which is both true
 * and free of any signal about where the site is operated from.
 */
export function ViewerTimezoneLabel() {
  const [zone, setZone] = useState("UTC");

  useEffect(() => {
    const resolved = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (resolved) setZone(resolved);
  }, []);

  return <span>Times in {zone}</span>;
}
