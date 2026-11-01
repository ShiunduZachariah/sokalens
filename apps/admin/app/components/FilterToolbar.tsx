type FilterToolbarProps = {
  title?: string;
  searchPlaceholder?: string;
  filters: string[];
  actions?: string[];
};

export function FilterToolbar({
  title = "Filters",
  searchPlaceholder = "Search records",
  filters,
  actions = []
}: FilterToolbarProps) {
  return (
    <div className="filter-toolbar" aria-label={title}>
      <label className="filter-toolbar__search">
        <span>Search</span>
        <input placeholder={searchPlaceholder} type="search" />
      </label>
      <div className="filter-toolbar__chips" aria-label="Filter options">
        {filters.map((filter) => (
          <button className="chip-button" key={filter} type="button">
            {filter}
          </button>
        ))}
      </div>
      {actions.length > 0 ? (
        <div className="filter-toolbar__actions">
          {actions.map((action, index) => (
            <button className={index === 0 ? "button-primary" : "button-secondary"} key={action} type="button">
              {action}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
