import { type ComponentProps } from 'react';
import { RotateCcw } from 'lucide-react';
import { Input } from './input.tsx';
import { slugify } from '../lib/slug.ts';

export interface SlugFieldProps {
  id: string;
  /** The field's own registration, exactly as every other input here
   * gets it — the slug stays uncontrolled, like its neighbours. */
  inputProps: ComponentProps<typeof Input>;
  /** Reads the name the slug is derived from, at the moment it is asked.
   * A getter rather than a value on purpose: subscribing to the name
   * would re-render this field on every keystroke in another one, and
   * react-hook-form's `watch` opts the whole component out of the React
   * compiler. Nothing here needs the name until somebody acts. */
  readSource: () => string;
  /** Writes the generated slug back into the form. */
  write: (slug: string) => void;
}

/**
 * SlugField is a slug input with a button that fills it from the name
 * beside it.
 *
 * Two behaviours, and the split is the point. On blur, an EMPTY slug is
 * filled from the source, so somebody who types a name and tabs past
 * gets a working one without being asked. A slug that already holds
 * something is never rewritten on its own: it may be a deliberate
 * choice, and silently following the name as it is edited is the
 * behaviour people learn to fight.
 *
 * The button is the other half — it overwrites, on purpose, because once
 * the field has been touched there is otherwise no way back. When the
 * name yields nothing slug-worthy the click does nothing rather than
 * clearing the field, which would leave somebody retyping what they had.
 */
export function SlugField({ id, inputProps, readSource, write }: SlugFieldProps) {
  return (
    <div className="flex items-center gap-2">
      <Input
        {...inputProps}
        id={id}
        onBlur={(e) => {
          inputProps.onBlur?.(e);
          if (e.target.value.trim() !== '') return;
          const generated = slugify(readSource());
          if (generated !== '') write(generated);
        }}
      />
      <button
        type="button"
        onClick={() => {
          const generated = slugify(readSource());
          if (generated !== '') write(generated);
        }}
        aria-label="Generate slug from the name"
        title="Generate from the name"
        className="shrink-0 rounded-md border border-border p-2 text-muted hover:text-foreground"
      >
        <RotateCcw className="size-4" aria-hidden />
      </button>
    </div>
  );
}
