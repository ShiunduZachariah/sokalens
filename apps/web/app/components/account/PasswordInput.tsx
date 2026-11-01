"use client";

import { useState, type InputHTMLAttributes } from "react";

type PasswordInputProps = Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
  hint?: string;
  label: string;
};

export function PasswordInput({ hint, id, label, ...inputProps }: PasswordInputProps) {
  const [visible, setVisible] = useState(false);
  return (
    <div className="field">
      <label htmlFor={id}>{label}</label>
      <div className="password-input">
        <input {...inputProps} id={id} type={visible ? "text" : "password"} />
        <button
          aria-label={visible ? "Hide password" : "Show password"}
          aria-pressed={visible}
          className="password-input__toggle"
          onClick={() => setVisible((value) => !value)}
          type="button"
        >
          <svg aria-hidden="true" viewBox="0 0 24 24">
            {visible ? (
              <path d="M3 3l18 18M10.6 10.7a2 2 0 002.7 2.7M9.9 4.3A10.8 10.8 0 0112 4c5.5 0 9 6 9 6a16.5 16.5 0 01-2.2 2.8M6.2 6.2C4.1 7.6 3 10 3 10s3.5 6 9 6c1.1 0 2.1-.2 3-.6" />
            ) : (
              <path d="M3 12s3.5-6 9-6 9 6 9 6-3.5 6-9 6-9-6-9-6zm9 3a3 3 0 100-6 3 3 0 000 6z" />
            )}
          </svg>
        </button>
      </div>
      {hint ? <span className="field-hint">{hint}</span> : null}
    </div>
  );
}
