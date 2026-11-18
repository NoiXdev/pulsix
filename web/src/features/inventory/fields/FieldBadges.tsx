import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';
import { ChevronDown } from 'lucide-react';
import { Popover } from '../../../ui/Popover.tsx';
import { cn } from '../../../lib/utils.ts';
import { typesFor } from '../../variables/fieldEditor.ts';
import { scopeOf } from '../../variables/paths.ts';
import type { FieldType, SchemaField } from '../../variables/types.ts';
import { TYPE_ICONS } from './typeIcons.ts';
import { useT } from '../../../i18n/useT.ts';

const BADGE = 'inline-flex shrink-0 items-center gap-1 rounded-full border px-2 py-0.5 text-[11px] font-medium whitespace-nowrap';
const CLICKABLE = 'cursor-pointer hover:border-primary focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary';

/** Badge is one of a field row's badges: a button while the schema can be
 * edited, plain text for a reader. */
function Badge({ readOnly, label, className, onClick, children }: { readOnly?: boolean; label: string; className: string; onClick: () => void; children: ReactNode }) {
  if (readOnly) {
    return (
      <span className={cn(BADGE, className)} aria-label={label} role="img">
        {children}
      </span>
    );
  }
  return (
    <button type="button" aria-label={label} className={cn(BADGE, CLICKABLE, className)} onClick={onClick}>
      {children}
    </button>
  );
}

/** ScopeBadge shows where a field's value is set; a click flips it between
 * the group and per host. A per-host field stands out. */
export function ScopeBadge({ field, readOnly, onToggle }: { field: SchemaField; readOnly?: boolean; onToggle: () => void }) {
  const f = useT().inventory.fields;
  const host = scopeOf(field) === 'host';
  const text = host ? f.scopeHost : f.scopeGroup;
  return (
    <Badge
      readOnly={readOnly}
      label={f.scopeOf(field.name, text)}
      onClick={onToggle}
      className={host ? 'border-[var(--px-accent)] bg-[color-mix(in_srgb,var(--px-accent)_12%,transparent)] text-[var(--px-accent)]' : 'border-border text-muted-foreground'}
    >
      {text}
    </Badge>
  );
}

/** RequiredBadge flips a field between required and optional; required is
 * highlighted. */
export function RequiredBadge({ field, readOnly, onToggle }: { field: SchemaField; readOnly?: boolean; onToggle: () => void }) {
  const f = useT().inventory.fields;
  const text = field.required ? f.required : f.optional;
  return (
    <Badge
      readOnly={readOnly}
      label={f.requiredOf(field.name, text)}
      onClick={onToggle}
      className={field.required ? 'border-primary bg-primary/15 text-foreground' : 'border-dashed border-border text-muted-foreground'}
    >
      {text}
    </Badge>
  );
}

/** TypeBadge shows a field's type; a click opens a menu of every type, each
 * with its icon and a line of help, and picking one sets it. */
export function TypeBadge({ field, readOnly, onPick }: { field: SchemaField; readOnly?: boolean; onPick: (type: FieldType) => void }) {
  const t = useT();
  const f = t.inventory.fields;
  const anchor = useRef<HTMLButtonElement>(null);
  const [open, setOpen] = useState(false);
  const Icon = TYPE_ICONS[field.type];
  const label = f.types[field.type].label;
  const content = (
    <>
      <Icon size={12} aria-hidden="true" />
      {label}
    </>
  );
  if (readOnly) {
    return (
      <span className={cn(BADGE, 'border-border font-mono text-muted-foreground')} role="img" aria-label={f.typeOf(field.name, label)}>
        {content}
      </span>
    );
  }
  const close = (refocus: boolean) => {
    setOpen(false);
    if (refocus) anchor.current?.focus();
  };
  return (
    <>
      <button
        ref={anchor}
        type="button"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={f.typeOf(field.name, label)}
        className={cn(BADGE, CLICKABLE, 'border-border text-foreground')}
        onClick={() => setOpen((o) => !o)}
      >
        {content}
        <ChevronDown size={12} aria-hidden="true" />
      </button>
      {open && (
        <Popover anchor={anchor} label={f.typeMenu(field.name)} onClose={(reason) => close(reason === 'escape')}>
          <TypeMenu
            label={f.typeMenu(field.name)}
            value={field.type}
            onPick={(type) => {
              if (type !== field.type) onPick(type);
              close(true);
            }}
          />
        </Popover>
      )}
    </>
  );
}

/** TypeMenu lists the types as menu radio items: the arrow keys, Home and
 * End move between them, and the current one has the focus first. */
function TypeMenu({ label, value, onPick }: { label: string; value: FieldType; onPick: (type: FieldType) => void }) {
  const f = useT().inventory.fields;
  const types = typesFor('group');
  const menu = useRef<HTMLDivElement>(null);
  useEffect(() => {
    menu.current?.querySelector<HTMLElement>('[aria-checked="true"]')?.focus();
  }, []);

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    const items = Array.from(e.currentTarget.querySelectorAll<HTMLElement>('[role="menuitemradio"]'));
    const at = items.indexOf(document.activeElement as HTMLElement);
    const next: Record<string, number> = { ArrowDown: at + 1, ArrowUp: at - 1, Home: 0, End: items.length - 1 };
    if (!(e.key in next)) return;
    e.preventDefault();
    items[(next[e.key] + items.length) % items.length]?.focus();
  }

  return (
    <div ref={menu} role="menu" aria-label={label} onKeyDown={onKeyDown} className="flex w-72 flex-col gap-0.5">
      {types.map((type) => {
        const Icon = TYPE_ICONS[type];
        const checked = type === value;
        return (
          <button
            key={type}
            type="button"
            role="menuitemradio"
            aria-checked={checked}
            onClick={() => onPick(type)}
            className={cn('flex items-start gap-2 rounded px-2 py-1.5 text-left hover:bg-accent focus-visible:bg-accent focus-visible:outline-none', checked && 'bg-primary/10')}
          >
            <Icon size={14} aria-hidden="true" className="mt-0.5 shrink-0 text-muted-foreground" />
            <span className="flex min-w-0 flex-col">
              <span className="text-xs font-medium">{f.types[type].label}</span>
              <span className="text-[11px] text-muted-foreground">{f.types[type].help}</span>
            </span>
          </button>
        );
      })}
    </div>
  );
}
