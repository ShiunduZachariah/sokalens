"use client";

import { useEffect, useState } from "react";

type FilterBarProps = {
  label: string;
  filters: string[];
  active?: string;
  onChange?: (filter: string) => void;
};

export function FilterBar({ label, filters, active = filters[0], onChange }: FilterBarProps) {
  const [selected, setSelected] = useState(active);

  useEffect(() => {
    setSelected(active);
  }, [active]);

  return (
    <div className="filter-bar" aria-label={label} role="group">
      {filters.map((filter) => (
        <button
          aria-pressed={filter === selected}
          className={filter === selected ? "filter-chip filter-chip--active" : "filter-chip"}
          key={filter}
          onClick={() => {
            setSelected(filter);
            onChange?.(filter);
          }}
          type="button"
        >
          {filter}
        </button>
      ))}
    </div>
  );
}
