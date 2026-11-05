/**
 * A variable schema (inventory groups, job templates, shared template
 * definitions), exactly as spec §6 and internal/inventory/schema define it.
 * The API sends and accepts this JSON unchanged; every variables form
 * renders from it.
 */
export type FieldType = 'string' | 'int' | 'float' | 'bool' | 'enum' | 'secret' | 'list' | 'repeater' | 'object';
export type FieldScope = 'group' | 'host';

/** Where a top-level field came from (spec 7.75 goal 2); absent means
 * manual. The generated API type says `string`; the server accepts only
 * these four (internal/inventory/schema.Origin). */
export type FieldOrigin = 'manual' | 'source' | 'adjusted' | 'gone';

/** The kinds of variable source a playbook declares a field in. */
export type FieldSourceKind = 'argument_spec' | 'vars_prompt' | 'marker' | 'sidecar';

/** Where a field from a playbook was read: the role and entry point for an
 * argument spec, and always the file (and line, when known). */
export interface FieldSource {
  kind: FieldSourceKind;
  role?: string;
  entry_point?: string;
  file: string;
  line?: number;
}

export interface SchemaField {
  name: string;
  label?: string;
  description?: string;
  type: FieldType;
  scope?: FieldScope;
  required?: boolean;
  overridable?: boolean;
  default?: unknown;
  options?: string[];
  min?: number;
  max?: number;
  pattern?: string;
  min_items?: number;
  max_items?: number;
  item?: SchemaField;
  fields?: SchemaField[];
  /** Provenance, top-level fields only. Absent `active` means active. */
  active?: boolean;
  origin?: FieldOrigin;
  source?: FieldSource;
  /** The playbook already sets this variable (templates only). */
  set_in_playbook?: boolean;
  /** The source's own version of an adjusted (or gone) field, as last read. */
  source_field?: SchemaField;
}

export interface VarSchema {
  fields: SchemaField[];
}

/** A vars document: group values or a membership's values. */
export type Vars = Record<string, unknown>;

/** One validation problem; path uses the server's format: vhosts[1].domain. */
export interface Problem {
  path: string;
  message: string;
}

/** Where the variable editor is used: an inventory group (scope, host
 * overrides, the assignment preview), a job template (origins from the
 * playbook) or a shared template definition (no secret type). */
export type EditorVariant = 'group' | 'template' | 'shared';
