"use client";

import { usePathname, useRouter } from "next/navigation";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";
import {
  accountApiRequest,
  accountApiRequestFirst,
  normalizeAccessContext,
  normalizeProfile,
  type AccountProfile,
  type AccessContext,
} from "../../data/accountApi";
import { isOddsFormat, writeOddsFormat } from "../../lib/odds";
import { localeFromPath, localePath, stripLocalePrefix } from "../../i18n/config";

type AccountContextValue = {
  access: AccessContext | null;
  hasEntitlement: (entitlement: string) => boolean;
  profile: AccountProfile | null;
  profileStatus: "idle" | "loading" | "ready" | "unavailable";
  refreshAccess: () => Promise<void>;
  refreshProfile: () => Promise<void>;
  status: "loading" | "ready" | "unavailable";
};

const AccountContext = createContext<AccountContextValue | null>(null);

export function AccountProvider({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const activePath = stripLocalePrefix(pathname);
  const locale = localeFromPath(pathname);
  const router = useRouter();
  const [access, setAccess] = useState<AccessContext | null>(null);
  const [profile, setProfile] = useState<AccountProfile | null>(null);
  const [profileStatus, setProfileStatus] = useState<AccountContextValue["profileStatus"]>("idle");
  const [status, setStatus] = useState<AccountContextValue["status"]>("loading");

  const refreshAccess = useCallback(async () => {
    setStatus((current) => current === "ready" ? current : "loading");
    const result = await accountApiRequestFirst<unknown>(["/v1/access", "/v1/access/context"]);
    if (!result.ok) {
      setAccess(null);
      setStatus("unavailable");
      return;
    }
    setAccess(normalizeAccessContext(result.data));
    setStatus("ready");
  }, []);

  const refreshProfile = useCallback(async () => {
    setProfileStatus("loading");
    const result = await accountApiRequest<unknown>("/v1/me");
    if (!result.ok) {
      setProfile(null);
      setProfileStatus("unavailable");
      return;
    }
    const next = normalizeProfile(result.data);
    setProfile(next);
    if (isOddsFormat(next.oddsFormat)) writeOddsFormat(next.oddsFormat);
    setProfileStatus("ready");
  }, []);

  useEffect(() => {
    void refreshAccess();
  }, [refreshAccess]);

  useEffect(() => {
    if (access?.authenticated) void refreshProfile();
    else {
      setProfile(null);
      setProfileStatus("idle");
    }
  }, [access?.authenticated, refreshProfile]);

  const oddsPreferenceRequired = Boolean(
    access?.authenticated && profileStatus === "ready" && profile && !profile.oddsFormatSelected && activePath !== "/odds-preference",
  );

  useEffect(() => {
    if (!oddsPreferenceRequired) return;
    const next = activePath === "/login" || activePath === "/register" || activePath === "/verify-email" ? "/account" : activePath;
    const destination = `/odds-preference?next=${encodeURIComponent(next)}`;
    router.replace(locale ? localePath(destination, locale) : destination);
  }, [activePath, locale, oddsPreferenceRequired, router]);

  const value = useMemo<AccountContextValue>(() => ({
    access,
    hasEntitlement: (entitlement) => Boolean(access?.entitlements.includes(entitlement)),
    profile,
    profileStatus,
    refreshAccess,
    refreshProfile,
    status,
  }), [access, profile, profileStatus, refreshAccess, refreshProfile, status]);

  return (
    <AccountContext.Provider value={value}>
      {oddsPreferenceRequired ? (
        <main className="preference-page" id="content"><section className="preference-panel"><p>Opening your odds preference…</p></section></main>
      ) : children}
    </AccountContext.Provider>
  );
}

export function useAccount() {
  const value = useContext(AccountContext);
  if (!value) throw new Error("useAccount must be used within AccountProvider");
  return value;
}
