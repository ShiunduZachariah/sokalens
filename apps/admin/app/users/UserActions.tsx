"use client";

import { useState } from "react";
import { useRouter } from "next/navigation";

export function UserActions({ userId, status }: { userId: number; status: string }) {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [message, setMessage] = useState("");
  async function act(action: "suspend" | "reactivate" | "sessions/revoke") {
    const reason = window.prompt(`Reason for ${action.replace("/", " ")}:`)?.trim();
    if (!reason) return;
    setPending(true); setMessage("");
    const response = await fetch(`/api/admin/users/${userId}/${action}`, {
      method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ reason, evidence: { source: "admin_portal" } }),
    }).catch(() => null);
    setPending(false);
    setMessage(response?.ok ? "Saved" : "Action failed");
    if (response?.ok) router.refresh();
  }
  return <div className="table-actions">
    <button className="button-secondary" disabled={pending} onClick={() => void act("sessions/revoke")} type="button">Revoke sessions</button>
    <button className={status === "suspended" ? "button-primary" : "button-secondary"} disabled={pending} onClick={() => void act(status === "suspended" ? "reactivate" : "suspend")} type="button">{status === "suspended" ? "Reactivate" : "Suspend"}</button>
    {message ? <span aria-live="polite">{message}</span> : null}
  </div>;
}

