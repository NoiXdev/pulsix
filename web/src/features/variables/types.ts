/**
 * The variable schema of an inventory group, exactly as spec §6 and
 * internal/inventory/schema define it. The API sends and accepts this JSON
 * unchanged; every form in the inventory UI renders from it.
 */
export type FieldType = 'string' | 'int' | 'float' | 'bool' | 'enum' | 'secret' | 'list' | 'repeater' | 'object';
export type FieldScope = 'group' | 'host';

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
