"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useState, type FormEvent } from "react";
import {
  accountApiRequest,
  asArray,
  asRecord,
  normalizeProfile,
  readBoolean,
  readString,
  type AccountProfile,
  type AccountSession,
} from "../../data/accountApi";
import { isOddsFormat, writeOddsFormat } from "../../lib/odds";
import { LoadingSkeleton } from "../LoadingSkeleton";
import { useAccount } from "./AccountContext";
import { InlineStatus } from "./InlineStatus";
import { PasswordInput } from "./PasswordInput";
import { deviceLabel } from "../../lib/deviceLabel";

function normalizeSessions(payload: unknown): AccountSession[] {
  const root = asRecord(payload);
  const values = asArray(root.sessions).length ? asArray(root.sessions) : asArray(payload);
  return values.map((value) => {
    const session = asRecord(value);
    return {
      current: readBoolean(session, "current", "is_current") ?? false,
      // device_name first, then the agent, then an honest "Unknown device".
      // Reading the raw agent straight into the label -- which is what the
      // fallback chain used to do -- put "Mozilla/5.0 (Windows NT 10.0;
      // Win64; x64; rv:156.0)" where a reader needs "Firefox on Windows".
      deviceName: deviceLabel(
        readString(session, "device_name", "deviceName"),
        readString(session, "user_agent", "userAgent"),
      ),
      expiresAt: readString(session, "expires_at", "expiresAt"),
      id: readString(session, "public_id", "id") ?? "",
      signedInAt: readString(session, "signed_in_at", "signedInAt", "created_at", "createdAt"),
    };
  }).filter((session) => session.id);
}

function dateTime(value?: string) {
  if (!value) return "Not available";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

export function AccountDashboard() {
  const router = useRouter();
  const { access, refreshAccess, refreshProfile, status } = useAccount();
  const [profile, setProfile] = useState<AccountProfile>();
  const [sessions, setSessions] = useState<AccountSession[]>([]);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState<string>();
  const [error, setError] = useState<string>();
  const [message, setMessage] = useState<string>();
  const [deleteConfirmation, setDeleteConfirmation] = useState("");

  useEffect(() => {
    if (status === "loading") return;
    if (!access?.authenticated) {
      setLoading(false);
      return;
    }
    let active = true;
    async function load() {
      const [profileResult, sessionsResult] = await Promise.all([
        accountApiRequest<unknown>("/v1/me"),
        accountApiRequest<unknown>("/v1/me/sessions"),
      ]);
      if (!active) return;
      if (!profileResult.ok) {
        setError(profileResult.message);
      } else {
        const normalized = normalizeProfile(profileResult.data);
        const root = asRecord(profileResult.data);
        const user = asRecord(root.user);
        if (!normalized.email) normalized.email = readString(user, "email") ?? "";
        if (!normalized.displayName) normalized.displayName = readString(user, "display_name", "name") ?? "";
        setProfile(normalized);
      }
      if (sessionsResult.ok) setSessions(normalizeSessions(sessionsResult.data));
      setLoading(false);
    }
    void load();
    return () => { active = false; };
  }, [access?.authenticated, status]);

  async function saveProfile(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!profile) return;
    setBusy("profile");
    setError(undefined);
    setMessage(undefined);
    const result = await accountApiRequest<unknown>("/v1/me/profile", {
      body: JSON.stringify({
        country_code: profile.countryCode || null,
        display_name: profile.displayName,
        preferred_language: profile.language,
        odds_format: profile.oddsFormat,
        personalization_enabled: profile.personalizationEnabled,
        timezone: profile.timezone,
      }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "PATCH",
    });
    setBusy(undefined);
    if (!result.ok) setError(result.message);
    else {
      if (isOddsFormat(profile.oddsFormat)) writeOddsFormat(profile.oddsFormat);
      await refreshProfile();
      setMessage("Profile and preferences saved.");
      await refreshAccess();
    }
  }

  async function revokeSession(id: string) {
    setBusy(id);
    setError(undefined);
    const result = await accountApiRequest<unknown>(`/v1/me/sessions/${encodeURIComponent(id)}`, {
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "DELETE",
    });
    setBusy(undefined);
    if (!result.ok) setError(result.message);
    else {
      setSessions((items) => items.filter((session) => session.id !== id));
      setMessage("Session signed out.");
    }
  }

  async function changePassword(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const form = new FormData(event.currentTarget);
    const currentPassword = String(form.get("current_password") ?? "");
    const newPassword = String(form.get("new_password") ?? "");
    const confirmation = String(form.get("new_password_confirmation") ?? "");
    if (newPassword !== confirmation) {
      setError("The new password confirmation does not match.");
      return;
    }
    setBusy("password");
    setError(undefined);
    setMessage(undefined);
    const result = await accountApiRequest<unknown>("/v1/me/password", {
      body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "POST",
    });
    setBusy(undefined);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    event.currentTarget.reset();
    await refreshAccess();
    router.replace("/login?next=/account");
    router.refresh();
  }

  async function signOut() {
    setBusy("logout");
    const result = await accountApiRequest<unknown>("/v1/auth/logout", {
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "POST",
    });
    setBusy(undefined);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    await refreshAccess();
    router.replace("/login");
    router.refresh();
  }

  async function deleteAccount() {
    if (deleteConfirmation !== "DELETE") return;
    setBusy("delete");
    setError(undefined);
    const result = await accountApiRequest<unknown>("/v1/me", {
      headers: access?.csrfToken ? { "X-CSRF-Token": access.csrfToken } : undefined,
      method: "DELETE",
    });
    setBusy(undefined);
    if (!result.ok) {
      setError(result.message);
      return;
    }
    await refreshAccess();
    router.replace("/");
    router.refresh();
  }

  if (loading || status === "loading") {
    return <LoadingSkeleton count={3} label="Loading your account" variant="card" />;
  }

  if (!access?.authenticated) {
    return (
      <section className="state-card account-state">
        <span className="eyebrow">Sign in required</span>
        <h2>Your account is ready when you are.</h2>
        <p className="muted">Sign in to review your profile, active sessions, privacy settings, and favorites.</p>
        {error ? <InlineStatus message={error} tone="error" /> : null}
        <div className="button-row">
          <Link className="button" href="/login?next=/account">Sign in</Link>
          <Link className="button button--secondary" href="/register">Create account</Link>
        </div>
      </section>
    );
  }

  return (
    <div className="account-layout">
      <div className="account-layout__main">
        {message ? <InlineStatus message={message} tone="success" /> : null}
        {error ? <InlineStatus message={error} tone="error" /> : null}

        <form className="account-section" id="profile" onSubmit={saveProfile}>
          <div className="account-section__heading">
            <div>
              <span className="eyebrow">Profile</span>
              <h2>Your public details</h2>
            </div>
            <span className={profile?.emailVerified ? "pill pill--pitch" : "pill pill--gold"}>
              {profile?.emailVerified ? "Email verified" : "Email not verified"}
            </span>
          </div>
          <div className="account-form-grid">
            <div className="field">
              <label htmlFor="profile-name">Display name</label>
              <input id="profile-name" maxLength={80} onChange={(event) => setProfile((value) => value ? { ...value, displayName: event.target.value } : value)} required value={profile?.displayName ?? ""} />
            </div>
            <div className="field">
              <label htmlFor="profile-email">Email</label>
              <input disabled id="profile-email" value={profile?.email ?? access.email ?? ""} />
              <span className="field-hint">Email changes require a separate verification flow.</span>
            </div>
            <div className="field">
              <label htmlFor="profile-country">Country code</label>
              <input
                autoCapitalize="characters"
                id="profile-country"
                maxLength={2}
                minLength={2}
                onChange={(event) => setProfile((value) => value ? { ...value, countryCode: event.target.value.toUpperCase() } : value)}
                pattern="[A-Za-z]{2}"
                required
                value={profile?.countryCode ?? ""}
              />
              <span className="field-hint">Use the two-letter code for your country.</span>
            </div>
            <div className="field">
              <label htmlFor="profile-timezone">Timezone</label>
              <select id="profile-timezone" onChange={(event) => setProfile((value) => value ? { ...value, timezone: event.target.value } : value)} value={profile?.timezone ?? "Africa/Nairobi"}>
                <option value="Africa/Nairobi">Africa/Nairobi</option>
                <option value="Africa/Lagos">Africa/Lagos</option>
                <option value="Europe/London">Europe/London</option>
                <option value="UTC">UTC</option>
              </select>
            </div>
            <div className="field">
              <label htmlFor="profile-odds">Odds format</label>
              <select id="profile-odds" onChange={(event) => setProfile((value) => value ? { ...value, oddsFormat: event.target.value } : value)} value={profile?.oddsFormat ?? "decimal"}>
                <option value="decimal">Decimal</option>
                <option value="fractional">Fractional</option>
                <option value="american">American</option>
              </select>
            </div>
          </div>
          <div className="account-section__actions">
            <button className="button" disabled={busy === "profile"} type="submit">{busy === "profile" ? "Saving…" : "Save changes"}</button>
            {!profile?.emailVerified ? <Link className="text-link" href="/verify-email">Verify email</Link> : null}
          </div>
        </form>

        <form className="account-section" id="password" onSubmit={changePassword}>
          <div className="account-section__heading">
            <div>
              <span className="eyebrow">Security</span>
              <h2>Change password</h2>
            </div>
          </div>
          <p className="muted">Use at least 8 characters. Changing your password signs out every active session.</p>
          <div className="account-form-grid">
            <PasswordInput autoComplete="current-password" id="current-password" label="Current password" name="current_password" required />
            <PasswordInput autoComplete="new-password" id="new-password" label="New password" minLength={8} name="new_password" required />
            <PasswordInput autoComplete="new-password" id="new-password-confirmation" label="Confirm new password" minLength={8} name="new_password_confirmation" required />
          </div>
          <div className="account-section__actions">
            <button className="button" disabled={busy === "password"} type="submit">{busy === "password" ? "Updating…" : "Update password"}</button>
          </div>
        </form>

        <section className="account-section" id="sessions" aria-labelledby="sessions-heading">
          <div className="account-section__heading">
            <div>
              <span className="eyebrow">Security</span>
              <h2 id="sessions-heading">Active sessions</h2>
            </div>
          </div>
          {sessions.length ? (
            <div className="session-list">
              {sessions.map((session) => (
                <article key={session.id}>
                  <div>
                    <strong>{session.deviceName}</strong>
                    <p className="micro">Signed in {dateTime(session.signedInAt)} · Expires {dateTime(session.expiresAt)}</p>
                  </div>
                  {session.current ? <span className="pill pill--pitch">This device</span> : (
                    <button className="button button--secondary button--small" disabled={busy === session.id} onClick={() => void revokeSession(session.id)} type="button">
                      {busy === session.id ? "Signing out…" : "Sign out"}
                    </button>
                  )}
                </article>
              ))}
            </div>
          ) : <p className="muted">Session details are not available right now.</p>}
          <button className="text-link text-link--button" disabled={busy === "logout"} onClick={() => void signOut()} type="button">
            {busy === "logout" ? "Signing out…" : "Sign out of this account"}
          </button>
        </section>

        <section className="account-section account-section--danger" id="privacy" aria-labelledby="privacy-heading">
          <div className="account-section__heading">
            <div>
              <span className="eyebrow">Privacy</span>
              <h2 id="privacy-heading">Delete account</h2>
            </div>
          </div>
          <p className="muted">This starts the approved account-deletion process. Some security, billing, or legal records may be retained where required.</p>
          <div className="danger-confirmation">
            <div className="field">
              <label htmlFor="delete-confirmation">Type DELETE to confirm</label>
              <input id="delete-confirmation" onChange={(event) => setDeleteConfirmation(event.target.value)} value={deleteConfirmation} />
            </div>
            <button className="button button--danger" disabled={deleteConfirmation !== "DELETE" || busy === "delete"} onClick={() => void deleteAccount()} type="button">
              {busy === "delete" ? "Submitting…" : "Delete account"}
            </button>
          </div>
        </section>
      </div>

      <aside className="account-layout__rail" aria-label="Account summary">
        <div className="rail-panel rail-panel--dark">
          <span className="eyebrow">Current access</span>
          <h2>Free account</h2>
          <p>Public predictions stay open, while your saved teams, leagues, and matches follow you across devices.</p>
          <Link className="button button--light" href="/favorites">View favorites</Link>
        </div>
        <div className="rail-panel">
          <span className="eyebrow">Email verification</span>
          <h2>{access.emailVerified ? "Email verified" : "Check your inbox"}</h2>
          <p className="muted">Verification confirms that the address belongs to you and protects access to saved account features.</p>
          {!access.emailVerified ? <Link className="text-link" href="/verify-email">Send a new verification link</Link> : null}
        </div>
      </aside>
    </div>
  );
}
