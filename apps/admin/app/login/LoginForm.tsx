"use client";

import { FormEvent, useState } from "react";
import { useRouter } from "next/navigation";

export function LoginForm() {
  const router = useRouter();
  const [pending, setPending] = useState(false);
  const [error, setError] = useState("");
  const [passwordVisible, setPasswordVisible] = useState(false);

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setPending(true);
    setError("");
    const form = new FormData(event.currentTarget);
    const response = await fetch("/api/admin/login", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        email: form.get("email"),
        password: form.get("password"),
      }),
    }).catch(() => null);
    if (!response?.ok) {
      setError("Sign-in failed. Check your credentials.");
      setPending(false);
      return;
    }
    router.replace("/");
    router.refresh();
  }

  return (
    <form className="admin-login-card" onSubmit={submit}>
      <div>
        <p className="eyebrow">Restricted administration</p>
        <h1>SokaLens Admin</h1>
        <p>Use your provisioned administrator account credentials.</p>
      </div>
      <label>
        <span>Email</span>
        <input autoComplete="username" name="email" required type="email" />
      </label>
      <label>
        <span>Password</span>
        <span className="admin-password-input">
          <input autoComplete="current-password" minLength={8} name="password" required type={passwordVisible ? "text" : "password"} />
          <button aria-label={passwordVisible ? "Hide password" : "Show password"} aria-pressed={passwordVisible} onClick={() => setPasswordVisible((value) => !value)} type="button">
            <svg aria-hidden="true" viewBox="0 0 24 24"><path d={passwordVisible ? "M3 3l18 18M10.6 10.7a2 2 0 002.7 2.7M9.9 4.3A10.8 10.8 0 0112 4c5.5 0 9 6 9 6a16.5 16.5 0 01-2.2 2.8M6.2 6.2C4.1 7.6 3 10 3 10s3.5 6 9 6c1.1 0 2.1-.2 3-.6" : "M3 12s3.5-6 9-6 9 6 9 6-3.5 6-9 6-9-6-9-6zm9 3a3 3 0 100-6 3 3 0 000 6z"} /></svg>
          </button>
        </span>
      </label>
      {error ? <p aria-live="polite" className="form-error">{error}</p> : null}
      <button className="button-primary" disabled={pending} type="submit">
        {pending ? "Verifying…" : "Sign in securely"}
      </button>
      <p className="admin-login-help">Admin accounts cannot be registered publicly. Contact a Super Admin for provisioning or session recovery.</p>
    </form>
  );
}
