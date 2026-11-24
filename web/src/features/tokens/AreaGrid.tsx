import { useT } from '../../i18n/useT.ts';
import { roleLabel } from '../../i18n/roleLabel.ts';
import { ACTIVITY_AREA, AREA_ROWS, CREDENTIALS_AREA, areaAllowed, toggleArea, type Area } from './areas.ts';

const CELL = 'px-3 py-1.5';

/**
 * AreaGrid is the assistant token's area choice: one row per subject with a
 * read and a write checkbox (for Jobs the second one requests runs, for
 * Monitoring it handles alerts), and the credential names and the activity
 * log on their own. Each area has a one-line description under its label.
 * Ticking write ticks read, unticking read unticks write; an area the chosen
 * role cannot cover is disabled, and a line under the grid says why.
 */
export function AreaGrid({
  role,
  value,
  onChange,
  error,
}: {
  role: string;
  value: Area[];
  onChange: (areas: Area[]) => void;
  error?: string;
}) {
  const t = useT();
  const a = t.tokens.areas;
  const held = new Set(value);
  const anyDisabled = [...AREA_ROWS.flatMap((r) => [r.read, r.write]), CREDENTIALS_AREA, ACTIVITY_AREA].some((area) => !areaAllowed(role, area));

  function box(area: Area, label: string) {
    const allowed = areaAllowed(role, area);
    return (
      <input
        type="checkbox"
        aria-label={label}
        checked={held.has(area) && allowed}
        disabled={!allowed}
        aria-describedby={allowed ? undefined : 'token_areas_role_hint'}
        onChange={(e) => onChange(toggleArea(value, area, e.target.checked))}
      />
    );
  }

  return (
    <fieldset className="flex flex-col gap-1.5" aria-describedby="token_areas_hint">
      <legend className="mb-1.5 text-xs font-semibold">{a.legend}</legend>
      <table className="w-full rounded-md border border-border text-xs">
        <thead className="text-[11px] font-semibold tracking-wide text-muted-foreground uppercase">
          <tr className="border-b border-border">
            <th className={`${CELL} text-left`}>{a.area}</th>
            <th className={`${CELL} w-24 text-center`}>{a.read}</th>
            <th className={`${CELL} w-28 text-center`}>{a.write}</th>
          </tr>
        </thead>
        <tbody>
          {AREA_ROWS.map((row) => {
            const name = a.rows[row.key];
            const second = row.key === 'jobs' ? a.request : row.key === 'monitoring' ? a.handle : a.write;
            return (
              <tr key={row.key} className="border-b border-border last:border-b-0">
                <td className={CELL}>
                  {name}
                  <span className="block text-[11px] font-normal text-muted-foreground">{a.descriptions[row.key]}</span>
                </td>
                <td className={`${CELL} text-center`}>{box(row.read, a.cell(name, a.read))}</td>
                <td className={`${CELL} text-center`}>
                  {row.key === 'jobs' ? (
                    <label className="inline-flex items-center gap-1.5">
                      {box(row.write, a.cell(name, second))}
                      <span aria-hidden="true" className="text-[11px] text-muted-foreground">
                        {a.request}
                      </span>
                    </label>
                  ) : (
                    box(row.write, a.cell(name, second))
                  )}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
      <label className="flex items-center gap-2 text-xs">
        {box(CREDENTIALS_AREA, a.credentials)}
        <span>
          <span aria-hidden="true">{a.credentials}</span>
          <span className="block text-[11px] text-muted-foreground">{a.descriptions.credentials}</span>
        </span>
      </label>
      <label className="flex items-center gap-2 text-xs">
        {box(ACTIVITY_AREA, a.activity)}
        <span>
          <span aria-hidden="true">{a.activity}</span>
          <span className="block text-[11px] text-muted-foreground">{a.descriptions.activity}</span>
        </span>
      </label>
      <span id="token_areas_hint" className="text-[11px] text-muted-foreground">
        {a.writeHint}
      </span>
      {anyDisabled && (
        <span id="token_areas_role_hint" className="text-[11px] text-muted-foreground">
          {a.roleHint(roleLabel(t, role))}
        </span>
      )}
      {error && (
        <span role="alert" className="text-[11px] text-destructive">
          {error}
        </span>
      )}
    </fieldset>
  );
}
