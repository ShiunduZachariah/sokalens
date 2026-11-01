"use client";

import type { ReactNode } from "react";

/** In-play predictions are public and must not wait for account resolution. */
export function InPlayGate({ children }: { children: ReactNode }) {
  return <>{children}</>;
}
