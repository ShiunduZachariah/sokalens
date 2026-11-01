"use client";

import { useCallback, useEffect, useRef, useState } from "react";

type TurnstileApi = {
  remove: (widgetId: string) => void;
  reset: (widgetId: string) => void;
  render: (container: HTMLElement, options: Record<string, unknown>) => string;
};

export type TurnstileState = "error" | "expired" | "loading" | "ready" | "verified";

function turnstileApi() {
  return (window as Window & { turnstile?: TurnstileApi }).turnstile;
}

export function TurnstileWidget({
  action,
  onStateChange,
  onTokenChange,
  resetSignal = 0,
}: {
  action: "login" | "register";
  onStateChange?: (state: TurnstileState) => void;
  onTokenChange: (token: string) => void;
  resetSignal?: number;
}) {
  const siteKey = process.env.NEXT_PUBLIC_TURNSTILE_SITE_KEY?.trim();
  const containerRef = useRef<HTMLDivElement>(null);
  const widgetIdRef = useRef<string | undefined>(undefined);
  const [loadRevision, setLoadRevision] = useState(0);
  const [state, setState] = useState<TurnstileState>("loading");
  const onTokenChangeRef = useRef(onTokenChange);
  const onStateChangeRef = useRef(onStateChange);

  const updateState = useCallback((nextState: TurnstileState) => {
    setState(nextState);
    onStateChangeRef.current?.(nextState);
  }, []);

  useEffect(() => {
    onTokenChangeRef.current = onTokenChange;
  }, [onTokenChange]);

  useEffect(() => {
    onStateChangeRef.current = onStateChange;
  }, [onStateChange]);

  useEffect(() => {
    if (!siteKey) return;
    updateState("loading");
    const render = () => {
      const api = turnstileApi();
      if (!api || !containerRef.current || widgetIdRef.current) return;
      widgetIdRef.current = api.render(containerRef.current, {
        action,
        callback: (value: string) => {
          onTokenChangeRef.current(value);
          updateState("verified");
        },
        "error-callback": () => {
          onTokenChangeRef.current("");
          updateState("error");
        },
        "expired-callback": () => {
          onTokenChangeRef.current("");
          updateState("expired");
        },
        "response-field": false,
        sitekey: siteKey,
        theme: "auto",
      });
      updateState("ready");
    };

    let script = document.querySelector<HTMLScriptElement>("script[data-sokalens-turnstile]");
    if (!script) {
      script = document.createElement("script");
      script.async = true;
      script.defer = true;
      script.dataset.sokalensTurnstile = "true";
      script.src = "https://challenges.cloudflare.com/turnstile/v0/api.js?render=explicit";
      document.head.appendChild(script);
    }
    const handleScriptError = () => updateState("error");
    if (turnstileApi()) render();
    else script.addEventListener("load", render, { once: true });
    script.addEventListener("error", handleScriptError, { once: true });

    return () => {
      script?.removeEventListener("load", render);
      script?.removeEventListener("error", handleScriptError);
      if (widgetIdRef.current) turnstileApi()?.remove(widgetIdRef.current);
      widgetIdRef.current = undefined;
      onTokenChangeRef.current("");
    };
  }, [action, loadRevision, siteKey, updateState]);

  useEffect(() => {
    if (widgetIdRef.current) {
      turnstileApi()?.reset(widgetIdRef.current);
      onTokenChangeRef.current("");
      updateState("ready");
    }
  }, [resetSignal, updateState]);

  function retry() {
    onTokenChangeRef.current("");
    if (widgetIdRef.current && turnstileApi()) {
      turnstileApi()?.reset(widgetIdRef.current);
      updateState("ready");
      return;
    }

    document.querySelector<HTMLScriptElement>("script[data-sokalens-turnstile]")?.remove();
    setLoadRevision((current) => current + 1);
  }

  if (!siteKey) return null;

  return (
    <div className="turnstile-field">
      <div ref={containerRef} aria-label="Security verification" />
      <div aria-live="polite" className="field-hint" role="status">
        {state === "loading" ? "Loading the security check…" : null}
        {state === "ready" ? "Complete the security check to continue." : null}
        {state === "verified" ? "Security check complete." : null}
        {state === "expired" ? "The security check expired. Please try it again." : null}
        {state === "error" ? "The security check could not load. Check your connection and retry." : null}
        {state === "expired" || state === "error" ? (
          <> <button className="text-link text-link--button" onClick={retry} type="button">Retry security check</button></>
        ) : null}
      </div>
    </div>
  );
}
