import { StatusBadge } from "./StatusBadge";

export function AdminTopbar() {
  return (
    <header className="admin-topbar">
      <label className="topbar-search">
        <span>Search admin</span>
        <input placeholder="Search matches, users, jobs, request IDs" type="search" />
      </label>
      <div className="topbar-actions">
        <button className="icon-button" title="Open alerts" type="button">
          AL
        </button>
        <button className="icon-button" title="Open settings" type="button">
          ST
        </button>
        <div className="admin-identity">
          <StatusBadge status="healthy">On call</StatusBadge>
          <span>Secure admin session</span>
          <form action="/api/admin/logout" method="post">
            <button className="button-secondary" type="submit">Sign out</button>
          </form>
        </div>
      </div>
    </header>
  );
}
