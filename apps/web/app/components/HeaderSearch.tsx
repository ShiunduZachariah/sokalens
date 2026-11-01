"use client";

import { usePathname } from "next/navigation";
import { SearchBox } from "./SearchBox";
import { stripLocalePrefix } from "../i18n/config";

export function HeaderSearch() {
  const pathname = usePathname();

  if (stripLocalePrefix(pathname) === "/search") {
    return <div className="header-search-spacer" aria-hidden="true" />;
  }

  return <SearchBox compact />;
}
