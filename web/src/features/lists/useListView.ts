import { useCallback, useEffect, useRef, useState } from 'react';
import { useNavigate, useSearch } from '@tanstack/react-router';
import { usePreferences, useSavePreferences } from '../../api/hooks/usePreferences.ts';
import type { ServerPageQuery } from '../../api/hooks/paging.ts';
import type { PageSize, Preferences } from '../../api/types.ts';
import { useLocale, useT } from '../../i18n/useT.ts';
import { useToast } from '../../ui/toastContext.ts';
import type { DataListColumn, DataListProps, Density, SortState } from '../../ui/data-list/index.ts';
import { pageRows, sortRows } from './clientRows.ts';
import { mergeColumns } from './columns.ts';
import { validateListSearch, type ListSearch } from './listSearch.ts';
import { LIST_COLUMNS, type ListId } from './registry.ts';

/** How long a burst of column changes waits before it is saved (one PUT). */
const COLUMN_SAVE_DELAY_MS = 400;

const PAGE_SIZES: readonly number[] = [10, 25, 50, 100];

export interface ListViewOptions<Row = unknown> {
  /** 'server': the server sorts and pages (use `serverQuery`); 'client':
   * the whole list is in the browser (use `view`). */
  mode: 'client' | 'server';
  /** Sort and page in the URL (default). A second list on a page passes
   * false: its sort and page live in the component. */
  urlState?: boolean;
  /** The list's filters, serialised. When it changes the list goes back to
   * page 1 (a new search on page 3 would otherwise show an empty page). */
  resetKey?: string;
  /** Client mode: breaks ties between equal sort values. Without it equal
   * rows keep the order they came in. */
  rowKey?: (row: Row) => string;
}

/** What `listProps` hands DataList. */
export type ListViewProps<Row> = Pick<
  DataListProps<Row>,
  'sort' | 'onSortChange' | 'paging' | 'visibleColumns' | 'onVisibleColumnsChange' | 'defaultColumns' | 'density'
>;

export interface ListView<Row> {
  sort: SortState | null;
  /** The page shown: the URL's, back to 1 after a resetKey change, and at
   * most the last page once the total is known. */
  page: number;
  perPage: PageSize;
  visibleColumns: string[];
  density: Density;
  /** Server mode: the query of the page to fetch. */
  serverQuery: ServerPageQuery;
  /** Client mode: the page's rows, sorted then sliced, and the whole
   * list's length. `undefined` (still loading) gives no rows and no total. */
  view(rows: readonly Row[] | undefined): { rows: Row[]; total: number | undefined };
  /** The DataList props this hook owns. `total` undefined means not known
   * yet (loading): the page is then not clamped. Call it while rendering the
   * component that called useListView. */
  listProps(total: number | undefined, countLabel: (n: number) => string): ListViewProps<Row>;
}

interface PendingColumns {
  ids: string[];
  /** The list's defaults: saved by removing the list's entry. */
  isDefault: boolean;
}

/**
 * The glue between DataList and where its state lives: sort and page in the
 * URL (`?sort=&order=&page=`, the defaults left out), page size, density and
 * the chosen columns in the user's preferences. Column changes are saved
 * 400 ms after the last one (one PUT for a burst, and at once when the list
 * goes away); a page-size change is saved at once and applies to every list.
 * Changing the sort, the page size or `resetKey` goes back to page 1; a page
 * past the last (a stale link, the last row deleted) is replaced by the
 * last page in the URL.
 */
export function useListView<Row>(listId: ListId, columns: DataListColumn<Row>[], opts: ListViewOptions<Row>): ListView<Row> {
  if (import.meta.env.DEV) assertRegistered(listId, columns);
  const urlState = opts.urlState ?? true;
  const t = useT();
  const { locale } = useLocale();
  const toast = useToast();
  const { prefs } = usePreferences();
  const save = useSavePreferences();
  const navigate = useNavigate();
  const urlSearch = useSearch({ strict: false }) as Record<string, unknown>;
  const [localSearch, setLocalSearch] = useState<ListSearch>({});
  const search = urlState ? validateListSearch(urlSearch) : localSearch;

  const sort: SortState | null =
    search.sort !== undefined && columns.some((c) => c.sortKey === search.sort) ? { key: search.sort, order: search.order ?? 'asc' } : null;
  const perPage = prefs.page_size;
  const rawPage = search.page ?? 1;

  // A changed resetKey shows page 1 at once (no request for the old page),
  // until the URL has caught up below.
  const [reset, setReset] = useState<{ key: string | undefined; stalePage?: number }>({ key: opts.resetKey });
  let stalePage = reset.stalePage;
  if (reset.key !== opts.resetKey) {
    stalePage = rawPage > 1 ? rawPage : undefined;
    setReset({ key: opts.resetKey, stalePage });
  } else if (stalePage !== undefined && rawPage !== stalePage) {
    stalePage = undefined;
    setReset({ key: opts.resetKey });
  }
  const wanted = stalePage !== undefined ? 1 : rawPage;

  // The total last handed to listProps or view, for clamping the page.
  const [knownTotal, setKnownTotal] = useState<number | undefined>(undefined);
  const learnTotal = (total: number | undefined) => {
    if (total !== undefined && total !== knownTotal) setKnownTotal(total);
  };
  const page = clampPage(wanted, knownTotal, perPage);

  const writeUrl = useCallback(
    (clean: ListSearch, replace: boolean) =>
      void navigate({
        to: '.',
        search: (prev: Record<string, unknown>) => ({ ...prev, ...clean }),
        replace,
        resetScroll: false,
      }),
    [navigate],
  );
  const write = (next: ListSearch) => {
    const clean = cleanSearch(next);
    if (urlState) writeUrl(clean, false);
    else setLocalSearch(clean);
  };

  // Put the page shown into the URL when it differs: after a reset or a
  // clamp, replacing the history entry (the stale page is not a place to
  // go back to).
  const fixPage = page !== rawPage;
  if (fixPage && !urlState) setLocalSearch(cleanSearch({ ...localSearch, page }));
  useEffect(() => {
    if (fixPage && urlState) writeUrl(cleanSearch({ sort: search.sort, order: search.order, page }), true);
  }, [fixPage, urlState, writeUrl, search.sort, search.order, page]);

  // Columns: the saved choice, or a change still waiting to be saved.
  const saved = prefs.lists[listId]?.columns;
  const [draft, setDraft] = useState<string[] | null>(null);
  const visibleColumns = mergeColumns(draft ?? saved, columns);
  const defaultColumns = mergeColumns(undefined, columns);

  const failed = useCallback(() => toast.show(t.lists.saveFailed, 'destructive'), [toast, t.lists.saveFailed]);

  const pending = useRef<PendingColumns | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const flush = useCallback(() => {
    clearTimeout(timer.current);
    timer.current = undefined;
    const next = pending.current;
    pending.current = null;
    if (!next) return;
    save((prev) => withColumns(prev, listId, next))
      .catch(failed)
      .finally(() => setDraft((d) => (d === next.ids ? null : d)));
  }, [save, listId, failed]);

  // A change still waiting when the list goes away is saved then.
  const flushRef = useRef(flush);
  useEffect(() => {
    flushRef.current = flush;
  }, [flush]);
  useEffect(() => () => flushRef.current(), []);

  const onVisibleColumnsChange = (ids: string[]) => {
    setDraft(ids);
    pending.current = { ids, isDefault: sameList(ids, defaultColumns) };
    clearTimeout(timer.current);
    timer.current = setTimeout(flush, COLUMN_SAVE_DELAY_MS);
  };

  const onPerPageChange = (n: number) => {
    if (!PAGE_SIZES.includes(n) || n === perPage) return;
    if (rawPage > 1) write({ sort: search.sort, order: search.order });
    save((prev) => ({ ...prev, page_size: n as PageSize })).catch(failed);
  };

  return {
    sort,
    page,
    perPage,
    visibleColumns,
    density: prefs.density,
    serverQuery: { page, per_page: perPage, ...(sort ? { sort: sort.key, order: sort.order } : {}) },
    view(rows) {
      if (rows === undefined) return { rows: [], total: undefined };
      learnTotal(rows.length);
      const sorted = sortRows(rows, columns, sort, opts.rowKey ?? noKey, locale);
      return { rows: pageRows(sorted, clampPage(wanted, rows.length, perPage), perPage), total: rows.length };
    },
    listProps(total, countLabel) {
      learnTotal(total);
      return {
        sort,
        onSortChange: (next) => write({ sort: next?.key, order: next?.order }),
        paging: {
          page: clampPage(wanted, total ?? knownTotal, perPage),
          perPage,
          total: total ?? 0,
          countLabel,
          onPageChange: (n) => write({ sort: search.sort, order: search.order, page: n }),
          onPerPageChange,
        },
        visibleColumns,
        onVisibleColumnsChange,
        defaultColumns,
        density: prefs.density,
      };
    },
  };
}

/** A list's keys as they go into the URL: the defaults as undefined, so
 * the router leaves them out. */
function cleanSearch(next: ListSearch): ListSearch {
  return {
    sort: next.sort,
    order: next.sort !== undefined ? next.order : undefined,
    page: next.page !== undefined && next.page > 1 ? next.page : undefined,
  };
}

function clampPage(page: number, total: number | undefined, perPage: number): number {
  if (total === undefined) return page;
  return Math.min(page, Math.max(1, Math.ceil(total / perPage)));
}

function withColumns(prev: Preferences, listId: ListId, next: PendingColumns): Preferences {
  const lists = { ...prev.lists };
  if (next.isDefault) delete lists[listId];
  else lists[listId] = { columns: next.ids };
  return { ...prev, lists };
}

function sameList(a: readonly string[], b: readonly string[]): boolean {
  return a.length === b.length && a.every((v, i) => v === b[i]);
}

function noKey(): string {
  return '';
}

function assertRegistered<Row>(listId: ListId, columns: DataListColumn<Row>[]) {
  const known = LIST_COLUMNS[listId];
  for (const c of columns) {
    if (!known.includes(c.id)) {
      throw new Error(`useListView: column "${c.id}" of list "${listId}" is not in the list registry (features/lists/registry.ts)`);
    }
  }
}
