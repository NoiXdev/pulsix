import { Pill, type Tone } from './badge.tsx';

/** VantageTableRow is one probing point, already worded by the caller. */
export interface VantageTableRow {
  key: string;
  name: string;
  status: { label: string; tone: Tone };
  response: string;
  age: string;
}

/**
 * VantageTable is the compact table of a synthetic check's probing points
 * on the host page (backlog 3.13a): who probed, what it saw, how fast and
 * how long ago. Presentational: the feature words every cell.
 */
export function VantageTable({
  label,
  columns,
  rows,
}: {
  /** The table's accessible name. */
  label: string;
  columns: { name: string; status: string; response: string; age: string };
  rows: readonly VantageTableRow[];
}) {
  const head = 'px-2 py-1 text-left text-[11px] font-semibold text-muted-foreground';
  const cell = 'px-2 py-1';
  return (
    <table aria-label={label} className="w-full border-collapse text-xs">
      <thead>
        <tr className="border-b border-border">
          <th scope="col" className={head}>{columns.name}</th>
          <th scope="col" className={head}>{columns.status}</th>
          <th scope="col" className={`${head} text-right`}>{columns.response}</th>
          <th scope="col" className={head}>{columns.age}</th>
        </tr>
      </thead>
      <tbody>
        {rows.map((r) => (
          <tr key={r.key} className="border-b border-border last:border-b-0">
            <td className={`${cell} text-foreground`}>{r.name}</td>
            <td className={cell}>
              <Pill tone={r.status.tone}>{r.status.label}</Pill>
            </td>
            <td className={`${cell} text-right font-mono tabular-nums`}>{r.response}</td>
            <td className={`${cell} text-muted-foreground`}>{r.age}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
