"use client";

import { useRouter } from "next/navigation";
import { useEffect } from "react";

export function ViewerTimezone({ timezone }: { timezone: string }) {
  const router = useRouter();
  useEffect(() => {
    const detected = Intl.DateTimeFormat().resolvedOptions().timeZone;
    if (!detected || detected === timezone) return;
    document.cookie = `sokalens_timezone=${encodeURIComponent(detected)}; Path=/; Max-Age=31536000; SameSite=Lax`;
    router.refresh();
  }, [router, timezone]);
  return null;
}
