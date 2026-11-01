"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, type FormEvent } from "react";
import { accountApiRequest, normalizeCountryCode, safeInternalHref } from "../../data/accountApi";
import { useAccount } from "./AccountContext";
import { countryOptions } from "./countryOptions";
import { InlineStatus } from "./InlineStatus";
import { PasswordInput } from "./PasswordInput";
import { TurnstileWidget, type TurnstileState } from "./TurnstileWidget";
import { localePath } from "../../i18n/config";
import { useLocale } from "../locale/LocaleProvider";

type AuthMode = "forgot" | "login" | "register" | "reset" | "verify";

type AuthFormProps = {
  mode: AuthMode;
  nextPath?: string;
  token?: string;
};

const endpointByMode: Record<AuthMode, string> = {
  forgot: "/v1/auth/password-reset/request",
  login: "/v1/auth/login",
  register: "/v1/auth/register",
  reset: "/v1/auth/password-reset/confirm",
  verify: "/v1/auth/email-verification/confirm",
};

function acquisitionPayload() {
  if (typeof window === "undefined") return {};
  try {
    const first = JSON.parse(window.localStorage.getItem("sokalens.acquisition.first") ?? "{}") as Record<string, unknown>;
    const current = new URL(window.location.href);
    const text = (key: string, maximum: number) => typeof first[key] === "string" ? String(first[key]).slice(0, maximum) : "";
    return {
      landing_page: text("landing_page", 512),
      referrer_host: text("referrer_host", 253),
      signup_page: current.pathname.slice(0, 512),
      utm_campaign: text("utm_campaign", 160),
      utm_content: text("utm_content", 160),
      utm_medium: text("utm_medium", 120),
      utm_source: text("utm_source", 120),
    };
  } catch {
    return { signup_page: window.location.pathname.slice(0, 512) };
  }
}

type SignupEvent = "registration_failed" | "registration_succeeded" | "signup_started" | "signup_viewed" | "turnstile_failed" | "turnstile_succeeded";

function trackSignupEvent(eventType: SignupEvent, locale: string, step: string, reasonCode = "") {
  const safeReasonCode = reasonCode.toLowerCase().replace(/[^a-z0-9_-]/g, "_").slice(0, 64);
  void accountApiRequest<void>("/v1/growth/events", {
    body: JSON.stringify({
      event_type: eventType,
      locale,
      reason_code: safeReasonCode,
      source: "web_registration",
      step,
    }),
    keepalive: true,
    method: "POST",
  });
}

export function AuthForm({ mode, nextPath, token = "" }: AuthFormProps) {
  const router = useRouter();
  const { refreshAccess } = useAccount();
  const { locale, t } = useLocale();
  const [busy, setBusy] = useState(false);
  const [challengeRevision, setChallengeRevision] = useState(0);
  const [challengeState, setChallengeState] = useState<TurnstileState>("loading");
  const [challengeToken, setChallengeToken] = useState("");
  const [message, setMessage] = useState<string>();
  const [error, setError] = useState<string>();
  const [errorCode, setErrorCode] = useState<string>();
  const signupStartedRef = useRef(false);
  const signupViewedRef = useRef(false);
  const turnstileEventRef = useRef<TurnstileState | undefined>(undefined);
  const challengeRequired = Boolean(process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY?.trim()) && (mode === "login" || mode === "register");
  const destination = safeInternalHref(nextPath, "/");
  const localizedDestination = localePath(destination, locale);
  const loginHref = localePath(destination === "/" ? "/login" : `/login?next=${encodeURIComponent(destination)}`, locale);
  const registerHref = localePath(destination === "/" ? "/register" : `/register?next=${encodeURIComponent(destination)}`, locale);

  useEffect(() => {
    if (challengeToken && errorCode === "bot_challenge_failed") {
      setError(undefined);
      setErrorCode(undefined);
    }
  }, [challengeToken, errorCode]);

  useEffect(() => {
    if (mode !== "register" || signupViewedRef.current) return;
    signupViewedRef.current = true;
    trackSignupEvent("signup_viewed", locale, "register_form");
  }, [locale, mode]);

  function markSignupStarted() {
    if (mode !== "register" || signupStartedRef.current) return;
    signupStartedRef.current = true;
    trackSignupEvent("signup_started", locale, "register_form");
  }

  function handleChallengeState(nextState: TurnstileState) {
    setChallengeState(nextState);
    if (mode !== "register" || turnstileEventRef.current === nextState) return;
    turnstileEventRef.current = nextState;
    if (nextState === "verified") trackSignupEvent("turnstile_succeeded", locale, "security_check");
    if (nextState === "error" || nextState === "expired") {
      trackSignupEvent("turnstile_failed", locale, "security_check", nextState);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(undefined);
    setErrorCode(undefined);
    setMessage(undefined);

    const form = new FormData(event.currentTarget);
    const password = String(form.get("password") ?? "");
    const passwordConfirmation = String(form.get("password_confirmation") ?? "");
    if ((mode === "register" || mode === "reset") && password !== passwordConfirmation) {
      setError("The password confirmation does not match.");
      if (mode === "register") trackSignupEvent("registration_failed", locale, "submit", "password_confirmation_mismatch");
      setBusy(false);
      return;
    }

    const body: Record<string, unknown> = {};
    if (form.has("email")) body.email = String(form.get("email") ?? "").trim();
    if (form.has("display_name")) body.display_name = String(form.get("display_name") ?? "").trim();
    if (form.has("country_code")) body.country_code = normalizeCountryCode(String(form.get("country_code") ?? ""));
    if (form.has("password")) body.password = password;
    if (token && (mode === "reset" || mode === "verify")) body.token = token;
    if (mode === "login" || mode === "register") {
      body.turnstile_token = challengeToken;
    }
    if (mode === "register") {
      Object.assign(body, acquisitionPayload());
      body.accept_terms = form.get("accept_terms") === "on";
      body.guest_language = locale;
      body.preferred_language = locale;
      body.website = String(form.get("website") ?? "");
    }

    const endpoint = mode === "verify" && !token
      ? "/v1/auth/email-verification/request"
      : endpointByMode[mode];
    const result = await accountApiRequest<unknown>(endpoint, {
      body: JSON.stringify(body),
      method: "POST",
    });
    setBusy(false);
    if (!result.ok) {
      if (mode === "login" || mode === "register") {
        setChallengeToken("");
        setChallengeRevision((current) => current + 1);
      }
      setErrorCode(result.code);
      setError(result.message);
      if (mode === "register") trackSignupEvent("registration_failed", locale, "submit", result.code ?? "request_failed");
      return;
    }

    if (mode === "login") {
      await refreshAccess();
      router.replace(localizedDestination);
      router.refresh();
      return;
    }
    if (mode === "register") {
      trackSignupEvent("registration_succeeded", locale, "submit");
      await refreshAccess();
      router.replace(localizedDestination);
      router.refresh();
      return;
    }
    if (mode === "verify" && token) {
      await refreshAccess();
      router.replace(localePath("/account", locale));
      router.refresh();
      return;
    }
    if (mode === "reset") {
      setMessage("Your password has been reset. Sign in with your new password.");
      return;
    }
    setMessage(mode === "forgot"
      ? "If an account matches that email, a password-reset link is on the way."
      : "If an account matches that email, a fresh verification link is on the way.");
  }

  const title = {
    forgot: t("auth.forgot.title"),
    login: t("auth.login.title"),
    register: t("auth.register.title"),
    reset: t("auth.reset.title"),
    verify: t("auth.verify.title"),
  }[mode];
  const summary = {
    forgot: t("auth.forgot.summary"),
    login: t("auth.login.summary"),
    register: t("auth.register.summary"),
    reset: t("auth.reset.summary"),
    verify: token ? t("auth.verify.token_summary") : t("auth.verify.request_summary"),
  }[mode];

  return (
    <main className="auth-page" id="content">
      <section className="auth-panel" aria-labelledby="auth-title">
        <div className="auth-panel__intro">
          <p className="page-kicker">SokaLens account</p>
          <h1 id="auth-title">{title}</h1>
          <p>{summary}</p>
        </div>

        {mode === "reset" && !token ? (
          <div className="stack">
            <InlineStatus message="This password-reset link is missing its one-time token. Request a fresh link." tone="error" />
            <Link className="button" href={localePath("/forgot-password", locale)}>Request a new link</Link>
          </div>
        ) : message ? (
          <div className="stack">
            <InlineStatus message={message} tone="success" />
            <Link className="button" href={mode === "register" ? loginHref : localePath("/login", locale)}>
              Continue to sign in
            </Link>
          </div>
        ) : (
          <form className="auth-form" onFocusCapture={markSignupStarted} onSubmit={submit}>
            {mode === "register" ? (
              <>
                <div className="field">
                  <label htmlFor="display-name">Display name</label>
                  <input autoComplete="name" id="display-name" maxLength={80} name="display_name" required />
                </div>
                <div className="field">
                  <label htmlFor="country-code">Country</label>
                  <select
                    autoComplete="country"
                    id="country-code"
                    name="country_code"
                    required
                  >
                    <option value="">Select your country</option>
                    {countryOptions.map((country) => (
                      <option key={country.code} value={country.code}>{country.name}</option>
                    ))}
                  </select>
                </div>
                <div aria-hidden="true" className="sr-only">
                  <label htmlFor="register-website">Website</label>
                  <input autoComplete="off" id="register-website" name="website" tabIndex={-1} type="text" />
                </div>
              </>
            ) : null}
            {!(mode === "verify" && token) && mode !== "reset" ? (
              <div className="field">
                <label htmlFor={`${mode}-email`}>Email</label>
                <input autoComplete="email" id={`${mode}-email`} inputMode="email" name="email" required type="email" />
              </div>
            ) : null}
            {mode === "login" || mode === "register" || mode === "reset" ? (
              <PasswordInput
                autoComplete={mode === "login" ? "current-password" : "new-password"}
                hint={mode !== "login" ? "Use at least 8 characters. A unique phrase is easiest to remember." : undefined}
                id={`${mode}-password`}
                label="Password"
                maxLength={128}
                minLength={mode === "login" ? undefined : 8}
                name="password"
                required
              />
            ) : null}
            {mode === "register" || mode === "reset" ? (
              <PasswordInput autoComplete="new-password" id={`${mode}-password-confirmation`} label="Confirm password" maxLength={128} minLength={8} name="password_confirmation" required />
            ) : null}
            {mode === "register" ? (
              <div className="choice-stack">
                <label className="check-choice">
                  <input name="accept_terms" required type="checkbox" />
                  <span>I agree to the <Link href={localePath("/terms", locale)}>Terms</Link> and acknowledge the <Link href={localePath("/privacy", locale)}>Privacy notice</Link>.</span>
                </label>
              </div>
            ) : null}
            {mode === "login" || mode === "register" ? (
              <TurnstileWidget
                action={mode}
                onStateChange={handleChallengeState}
                onTokenChange={setChallengeToken}
                resetSignal={challengeRevision}
              />
            ) : null}
            {error ? <InlineStatus message={error} tone="error" /> : null}
            <button className="button button--wide" disabled={busy || (challengeRequired && !challengeToken)} type="submit">
              {busy ? "Please wait…" : challengeRequired && !challengeToken ? (
                challengeState === "loading"
                  ? "Loading security check…"
                  : challengeState === "error" || challengeState === "expired"
                    ? "Security check required"
                    : "Complete security check to continue"
              ) : {
                forgot: "Send reset link",
                login: "Sign in",
                register: "Create account",
                reset: "Reset password",
                verify: token ? "Verify email" : "Send a new link",
              }[mode]}
            </button>
          </form>
        )}

        <div className="auth-panel__footer">
          {mode === "login" ? (
            <><span>New to SokaLens? <Link href={registerHref}>Create an account</Link></span><Link href={localePath("/forgot-password", locale)}>Forgot password?</Link></>
          ) : null}
          {mode === "register" ? <span>Already registered? <Link href={loginHref}>Sign in</Link></span> : null}
          {mode !== "login" && mode !== "register" ? <Link href={localePath("/login", locale)}>Back to sign in</Link> : null}
        </div>
      </section>
      <aside className="auth-aside" aria-label="Account benefits">
        <span className="eyebrow">One account, clearer signals</span>
        <h2>Keep your match intelligence organised.</h2>
        <ul className="check-list">
          <li>Save teams, leagues, and matches</li>
          <li>Keep your favorites available across devices</li>
          <li>Control your profile and privacy</li>
          <li>Review and revoke active sessions</li>
        </ul>
        <p className="micro">Predictions remain statistical estimates, never guaranteed outcomes.</p>
      </aside>
    </main>
  );
}
