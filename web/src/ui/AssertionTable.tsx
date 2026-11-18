import { Fragment, type ReactNode } from 'react';
import { Pill, type Tone } from './badge.tsx';

/** AssertionTableRow is one assertion, already worded by the caller. */
export interface AssertionTableRow {
  key: string;
  name: string;
  expression: string;
  severity: string;
  status: { label: string; tone: Tone };
  actual: string;
  age: string;
  /** The row's actions; the table gets an actions column when any row has some. */
  actions?: ReactNode;
  /** Shown under the row, across the table's width (an ignored marker, an opened panel). */
  detail?: ReactNode;
}

/**
 * AssertionTable is the compact table of an http check's assertions on
 * the host page (backlog 3.13b): what each one states, how bad its failure
 * is, what the latest attempt made of it and the value it saw.
 * Presentational: the feature words every cell.
 */
export function AssertionTable({
  label,
  columns,
  rows,
}: {
  /** The table's accessible name. */
  label: string;
  columns: { name: string; expression: string; severity: string; status: string; actual: string; age: string; actions?: string };
  rows: readonly AssertionTableRow[];
}) {
  const head = 'px-2 py-1 text-left text-[11px] font-semibold text-muted-foreground';
  const hasActions = rows.some((r) => r.actions !== undefined);
  const cell = 'px-2 py-1 align-top';
  return (
    <table aria-label={label} className="w-full border-collapse text-xs">
      <thead>
        <tr className="border-b border-border">
          <th scope="col" className={head}>{columns.name}</th>
          <th scope="col" className={head}>{columns.expression}</th>
          <th scope="col" className={head}>{columns.severity}</th>
          <th scope="col" className={head}>{columns.status}</th>
          <th scope="col" className={head}>{columns.actual}</th>
          <th scope="col" className={head}>{columns.age}</th>
          {hasActions && <th scope="col" className={head}>{columns.actions}</th>}
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <Fragment key={r.key}>
            <tr className={`border-b border-border ${r.detail === undefined ? 'last:border-b-0' : ''}`}>
              <td className={`${cell} font-mono text-foreground`}>{r.name}</td>
              <td className={`${cell} break-all font-mono text-muted-foreground`}>{r.expression}</td>
              <td className={cell}>{r.severity}</td>
              <td className={cell}>
                <Pill tone={r.status.tone}>{r.status.label}</Pill>
              </td>
              <td className={`${cell} break-all font-mono`}>{r.actual}</td>
              <td className={`${cell} text-muted-foreground`}>{r.age}</td>
              {hasActions && <td className={cell}>{r.actions}</td>}
            </tr>
            {r.detail !== undefined && (
              <tr className="border-b border-border last:border-b-0">
                <td colSpan={hasActions ? 7 : 6} className="px-2 pb-1">
                  {r.detail}
                </td>
              </tr>
            )}
          </Fragment>
        ))}
      </tbody>
    </table>
  );
}
