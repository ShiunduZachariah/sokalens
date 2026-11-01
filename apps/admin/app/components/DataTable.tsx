import type { ReactNode } from "react";

export type DataTableColumn<T extends object> = {
  key: keyof T & string;
  header: string;
  align?: "left" | "center" | "right";
  priority?: "primary" | "secondary" | "optional";
  render?: (row: T) => ReactNode;
};

type DataTableProps<T extends object> = {
  columns: DataTableColumn<T>[];
  rows: T[];
  caption: string;
  emptyLabel?: string;
  getRowKey?: (row: T, index: number) => string;
};

export function DataTable<T extends object>({
  columns,
  rows,
  caption,
  emptyLabel = "No rows match the current filters.",
  getRowKey
}: DataTableProps<T>) {
  return (
    <div className="table-wrap">
      <table className="data-table">
        <caption>{caption}</caption>
        <thead>
          <tr>
            {columns.map((column) => (
              <th
                className={`is-${column.align ?? "left"} priority-${column.priority ?? "primary"}`}
                key={column.key}
                scope="col"
              >
                {column.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.length === 0 ? (
            <tr>
              <td className="data-table__empty" colSpan={columns.length}>
                {emptyLabel}
              </td>
            </tr>
          ) : (
            rows.map((row, rowIndex) => (
              <tr key={getRowKey?.(row, rowIndex) ?? rowIndex}>
                {columns.map((column) => (
                  <td
                    className={`is-${column.align ?? "left"} priority-${column.priority ?? "primary"}`}
                    data-label={column.header}
                    key={column.key}
                  >
                    {column.render ? column.render(row) : String(row[column.key] ?? "")}
                  </td>
                ))}
              </tr>
            ))
          )}
        </tbody>
      </table>
    </div>
  );
}
