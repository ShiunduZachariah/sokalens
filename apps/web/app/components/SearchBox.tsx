"use client";

import Link from "next/link";
import { useEffect, useRef, useState } from "react";
import { entityTypeLabel } from "../lib/entityType";
import { localePath } from "../i18n/config";
import { useLocale } from "./locale/LocaleProvider";
import { searchHitHref, type SearchHit } from "../lib/searchHref";

type SearchBoxProps = {
  initialQuery?: string;
  compact?: boolean;
};

type Suggestion = SearchHit & {
  id: number;
  subtitle: string;
};

export function SearchBox({ initialQuery = "", compact = false }: SearchBoxProps) {
  const { locale, t } = useLocale();
  const [query, setQuery] = useState(initialQuery);
  const [isActive, setIsActive] = useState(false);
  const [suggestions, setSuggestions] = useState<Suggestion[]>([]);
  const containerRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    setQuery(initialQuery);
  }, [initialQuery]);

  // Close on a pointer press outside, not on blur.
  //
  // Blur was asking `relatedTarget` whether focus had moved to something
  // inside the dropdown. Desktop Chrome focuses an anchor on mousedown so that
  // held up there, but Safari and Firefox do not focus anchors on click and
  // touch browsers do not either: `relatedTarget` came back null, the dropdown
  // unmounted between press and click, and the click landed on nothing. A
  // pointerdown check does not depend on where focus went.
  useEffect(() => {
    if (!isActive) return;
    const onPointerDown = (event: PointerEvent | MouseEvent) => {
      const target = event.target;
      if (target instanceof Node && containerRef.current?.contains(target)) return;
      setIsActive(false);
    };
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") setIsActive(false);
    };
    document.addEventListener("pointerdown", onPointerDown, true);
    document.addEventListener("keydown", onKeyDown);
    return () => {
      document.removeEventListener("pointerdown", onPointerDown, true);
      document.removeEventListener("keydown", onKeyDown);
    };
  }, [isActive]);

  useEffect(() => {
    const normalized = query.trim();
    // Three, matching the API. Firing at two spent a round trip per session
    // on a request that could only ever come back empty.
    if (normalized.length < 3) {
      setSuggestions([]);
      return;
    }
    const controller = new AbortController();
    const timer = setTimeout(async () => {
      try {
        const response = await fetch(
          `/api/search/suggest?q=${encodeURIComponent(normalized)}&limit=${compact ? 4 : 8}`,
          {
            signal: controller.signal,
            headers: { Accept: "application/json" },
          },
        );
        if (response.ok) {
          const payload = (await response.json()) as { results?: Suggestion[] };
          setSuggestions(payload.results ?? []);
        } else {
          setSuggestions([]);
        }
      } catch {
        setSuggestions([]);
      }
    }, 140);
    return () => {
      clearTimeout(timer);
      controller.abort();
    };
  }, [compact, query]);

  return (
    <div
      className="search-box"
      ref={containerRef}
      onFocus={() => setIsActive(true)}
    >
      <form action={localePath("/search", locale)} role="search">
        <input
          aria-label={t("search.aria")}
          name="q"
          onChange={(event) => {
            setQuery(event.target.value);
            // Typing reopens the list. Focus alone is not enough: after
            // Escape the input still has focus, so no focus event would fire
            // again and the dropdown would stay shut for the rest of the
            // session.
            setIsActive(true);
          }}
          onClick={() => setIsActive(true)}
          placeholder={t("search.placeholder")}
          type="search"
          value={query}
        />
        <button type="submit">{t("search.submit")}</button>
      </form>
      {isActive && suggestions.length > 0 ? (
        <div aria-label="Search suggestions" className="search-suggestions">
          {suggestions.map((suggestion) => (
            <Link
              aria-label={`${suggestion.name}, ${entityTypeLabel(suggestion.type)}`}
              href={localePath(searchHitHref(suggestion), locale)}
              key={`${suggestion.type}-${suggestion.key ?? suggestion.id}`}
              onClick={() => setIsActive(false)}
            >
              <span className="search-suggestions__type">{entityTypeLabel(suggestion.type)}</span>
              <strong>{suggestion.name}</strong>
              <span className="micro">
                {suggestion.subtitle}
              </span>
            </Link>
          ))}
        </div>
      ) : null}
    </div>
  );
}
