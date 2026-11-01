"use client";

import { useEffect, useState } from "react";

type Theme = "system" | "light" | "dark";

function readTheme(): Theme {
  const stored = window.localStorage.getItem("sokalens.theme");
  return stored === "light" || stored === "dark" ? stored : "system";
}

function applyTheme(theme: Theme): void {
  const resolved = theme === "system"
    ? (window.matchMedia("(prefers-color-scheme: dark)").matches ? "dark" : "light")
    : theme;
  document.documentElement.setAttribute("data-theme", resolved);
  if (theme === "system") window.localStorage.removeItem("sokalens.theme");
  else window.localStorage.setItem("sokalens.theme", theme);
}

const ORDER: Theme[] = ["system", "light", "dark"];
const LABEL: Record<Theme, string> = { system: "System", light: "Light", dark: "Dark" };

export function ThemeToggle() {
  const [theme, setTheme] = useState<Theme>("system");

  useEffect(() => {
    setTheme(readTheme());
  }, []);

  const cycle = () => {
    const idx = ORDER.indexOf(theme);
    const nextTheme = ORDER[(idx + 1) % ORDER.length];
    setTheme(nextTheme);
    applyTheme(nextTheme);
  };

  return (
    <button type="button" className="theme-toggle" onClick={cycle} aria-label={"Theme: " + LABEL[theme]} title={"Theme: " + LABEL[theme]}>
      {LABEL[theme]}
    </button>
  );
}
