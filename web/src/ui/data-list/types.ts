import type { ReactNode } from 'react';
import type { LinkProps } from '@tanstack/react-router';

/** How much air a list's rows get. Set once on the document
 * (`data-density`), read by DataList rows through CSS variables, and handed
 * to each cell's render for what CSS cannot do (an address under a name). */
export type Density = 'comfortable' | 'standard' | 'compact';

export type SortOrder = 'asc' | 'desc';

/** The list's sort: a column's `sortKey` and a direction. `null` where a
 * SortState is accepted means the list's own default order. */
export interface SortState {
  key: string;
  order: SortOrder;
}

/** One column of a DataList. */
export interface DataListColumn<Row> {
  id: string;
  /** Header text; '' for an icon column, which `menuLabel` then names. */
  label: string;
  /** The column's name in the Columns menu and to screen readers when
   * `label` is empty. */
  menuLabel?: string;
  /** A CSS grid track: '100px', 'minmax(0,1fr)'. */
  width: string;
  /** The key the list is sorted by; absent, the column is not sortable. */
  sortKey?: string;
  /** What a client-side sort compares (a whole list sorted in the browser). */
  sortValue?: (row: Row) => string | number | boolean | null | undefined;
  /** The name column: always shown, always first, and the cell that holds
   * the row's link or activation button. */
  required?: boolean;
  defaultVisible: boolean;
  align?: 'start' | 'end';
  /** A cell's content. A link or button inside it must carry
   * `relative z-10` to sit above the row's stretched link or button. */
  render: (row: Row, ctx: { density: Density }) => ReactNode;
}

/** One entry of a row's menu: an action, or a link (a download, a page). */
export type RowAction =
  | { kind?: 'action'; id: string; label: string; onSelect: () => void; destructive?: boolean; disabled?: boolean }
  | { kind: 'link'; id: string; label: string; href: string; download?: string };

/** Where a row leads, and the link's accessible name. */
export interface RowLink {
  to: LinkProps['to'];
  params: LinkProps['params'];
  search?: LinkProps['search'];
  hash?: LinkProps['hash'];
  label: string;
}

export interface DataListPaging {
  page: number;
  perPage: number;
  total: number;
  /** The total with its noun: (137) => '137 hosts'. */
  countLabel: (n: number) => string;
  onPageChange: (page: number) => void;
  onPerPageChange: (n: number) => void;
}

export interface DataListProps<Row> {
  /** The table's accessible name. */
  label: string;
  /** Every column the list offers, shown or not. */
  columns: DataListColumn<Row>[];
  rows: readonly Row[];
  rowKey: (row: Row) => string;
  /** Where a row leads; undefined for a row that opens nothing. */
  rowLink?: (row: Row) => RowLink | undefined;
  /** A row's name for its "…" button ("Actions for web-01") and its
   * activation button; falls back to the row link's label. */
  rowLabel?: (row: Row) => string;
  /** What opens a row that has no page of its own (an edit dialog): the
   * name cell becomes a button stretched over the row, named by
   * `rowLabel`. A row with a `rowLink` keeps its link instead. */
  onRowActivate?: (row: Row) => void;
  /** Whether `onRowActivate` applies to a row (a token that can no longer
   * be edited); absent, every row is activatable. */
  isRowActivatable?: (row: Row) => boolean;
  /** One source for the "…" menu and the context menu. A row whose list is
   * empty gets neither. */
  rowActions?: (row: Row) => RowAction[];
  sort?: SortState | null;
  /** Absent: no column header is a sort button. */
  onSortChange?: (next: SortState | null) => void;
  /** Absent: no footer. */
  paging?: DataListPaging;
  /** The ids of the shown columns, in display order. DataList renders them
   * as given (ids it has no column for are skipped); keeping the required
   * column first is the caller's (mergeColumns). */
  visibleColumns: string[];
  onVisibleColumnsChange: (ids: string[]) => void;
  /** What "Restore defaults" in the Columns menu sets. */
  defaultColumns: string[];
  density: Density;
  /** Nothing to show yet: a loading state replaces `empty`; rows already
   * there stay, and the table is marked busy. */
  loading?: boolean;
  /** Shown when there are no rows and nothing is loading. */
  empty: ReactNode;
  /** Search and filters, left of the Columns button. */
  toolbar?: ReactNode;
}
