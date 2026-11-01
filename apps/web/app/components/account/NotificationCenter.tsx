"use client";

import Link from "next/link";
import { useEffect, useState, type FormEvent } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  readBoolean,
  readString,
  safeInternalHref,
  type NotificationItem,
  type NotificationPreferences,
} from "../../data/accountApi";
import { EmptyState } from "../EmptyState";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";

const defaultPreferences: NotificationPreferences = {
  digestEnabled: false,
  favoriteAlerts: true,
  inAppEnabled: true,
  kickoffReminders: false,
};

function normalizeNotifications(payload: unknown) {
  const root = asRecord(payload);
  const preferences = asRecord(root.preferences);
  const values = asArray(root.notifications).length ? asArray(root.notifications) : asArray(root.items);
  return {
    items: values.map((value): NotificationItem => {
      const item = asRecord(value);
      return {
        createdAt: readString(item, "created_at", "createdAt"),
        href: readString(item, "href", "url"),
        id: readString(item, "id", "notification_id") ?? "",
        message: readString(item, "message", "body") ?? "A saved football update is ready.",
        read: readBoolean(item, "read", "is_read") ?? false,
        title: readString(item, "title", "subject") ?? "SokaLens update",
      };
    }).filter((item) => item.id),
    preferences: {
      digestEnabled: readBoolean(preferences, "digest_enabled", "digestEnabled") ?? defaultPreferences.digestEnabled,
      favoriteAlerts: readBoolean(preferences, "favorite_alerts", "favoriteAlerts") ?? defaultPreferences.favoriteAlerts,
      inAppEnabled: readBoolean(preferences, "in_app_enabled", "inAppEnabled") ?? defaultPreferences.inAppEnabled,
      kickoffReminders: readBoolean(preferences, "kickoff_reminders", "kickoffReminders") ?? defaultPreferences.kickoffReminders,
    },
  };
}

function dateTime(value?: string) {
  if (!value) return "Recently";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString([], { dateStyle: "medium", timeStyle: "short" });
}

export function NotificationCenter() {
  const { access, status } = useAccount();
  const [items, setItems] = useState<NotificationItem[]>([]);
  const [preferences, setPreferences] = useState(defaultPreferences);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();

  useEffect(() => {
    if (status === "loading") return;
    if (!access?.authenticated) {
      setLoading(false);
      return;
    }
    let active = true;
    void accountApiRequest<unknown>("/v1/me/notifications?limit=30").then((result) => {
      if (!active) return;
      if (result.ok) {
        const normalized = normalizeNotifications(result.data);
        setItems(normalized.items);
        setPreferences(normalized.preferences);
      } else setError(result.message);
      setLoading(false);
    });
    return () => { active = false; };
  }, [access?.authenticated, status]);

  async function savePreferences(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError(undefined);
    setMessage(undefined);
    const result = await accountApiRequest<unknown>("/v1/me/notifications", {
      body: JSON.stringify({
        digest_enabled: preferences.digestEnabled,
        favorite_alerts: preferences.favoriteAlerts,
        in_app_enabled: preferences.inAppEnabled,
        kickoff_reminders: preferences.kickoffReminders,
      }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "PATCH",
    });
    setBusy(false);
    if (!result.ok) setError(result.message);
    else setMessage("Notification preferences saved.");
  }

  async function markRead(item: NotificationItem) {
    if (item.read) return;
    const result = await accountApiRequest<unknown>(`/v1/me/notifications/${encodeURIComponent(item.id)}`, {
      body: JSON.stringify({ read: true }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "PATCH",
    });
    if (result.ok) setItems((current) => current.map((value) => value.id === item.id ? { ...value, read: true } : value));
  }

  if (loading || status === "loading") return <LoadingSkeleton count={4} label="Loading notifications" variant="row" />;
  if (!access?.authenticated) return <EmptyState actionHref="/login?next=/notifications" actionLabel="Sign in" message="Notifications are attached to an account so you stay in control across devices." title="Sign in for notifications" />;

  return (
    <div className="account-layout">
      <section className="account-section" aria-labelledby="notification-feed-heading">
        <div className="account-section__heading">
          <div>
            <span className="eyebrow">Inbox</span>
            <h2 id="notification-feed-heading">Recent updates</h2>
          </div>
          <span className="pill">{items.filter((item) => !item.read).length} unread</span>
        </div>
        {error ? <InlineStatus message={error} tone="error" /> : null}
        {items.length ? (
          <div className="notification-list">
            {items.map((item) => (
              <article className={item.read ? "notification-item" : "notification-item notification-item--unread"} key={item.id}>
                <span className="notification-item__dot" aria-hidden="true" />
                <div>
                  <h3>{item.title}</h3>
                  <p>{item.message}</p>
                  <span className="micro">{dateTime(item.createdAt)}</span>
                </div>
                <div className="notification-item__actions">
                  {item.href ? <Link className="text-link" href={safeInternalHref(item.href)}>Open</Link> : null}
                  {!item.read ? <button className="text-link text-link--button" onClick={() => void markRead(item)} type="button">Mark read</button> : null}
                </div>
              </article>
            ))}
          </div>
        ) : <EmptyState message="Updates for saved football and enabled reminders will appear here." title="You're all caught up" />}
      </section>

      <form className="account-section account-layout__rail" onSubmit={savePreferences}>
        <div className="account-section__heading">
          <div>
            <span className="eyebrow">Preferences</span>
            <h2>Choose useful updates</h2>
          </div>
        </div>
        <div className="choice-stack">
          {([
            ["inAppEnabled", "In-app updates", "Show important saved-football updates here."],
            ["favoriteAlerts", "Favorite alerts", "Notify me about teams, leagues, and matches I save."],
            ["kickoffReminders", "Kickoff reminders", "Remind me before selected matches begin."],
            ["digestEnabled", "Digest", "Group lower-priority updates into a digest."],
          ] as const).map(([key, label, description]) => (
            <label className="preference-choice" key={key}>
              <span><strong>{label}</strong><small>{description}</small></span>
              <input checked={preferences[key]} onChange={(event) => setPreferences((value) => ({ ...value, [key]: event.target.checked }))} type="checkbox" />
            </label>
          ))}
        </div>
        {message ? <InlineStatus message={message} tone="success" /> : null}
        <button className="button" disabled={busy} type="submit">{busy ? "Saving…" : "Save preferences"}</button>
        <p className="micro">Email or real-time options only activate when the server grants that capability.</p>
      </form>
    </div>
  );
}
