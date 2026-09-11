export interface TreeNode {
  id: string;
  name: string;
  parent_id?: string | null;
}

export interface TreeRow<T> {
  group: T;
  depth: number;
}

/** orderTree lists groups depth first with children sorted by name; a
 * group whose parent is missing is shown as a root. */
export function orderTree<T extends TreeNode>(groups: readonly T[]): TreeRow<T>[] {
  const ids = new Set(groups.map((g) => g.id));
  const byParent = new Map<string, T[]>();
  for (const g of groups) {
    const key = g.parent_id && ids.has(g.parent_id) ? g.parent_id : '';
    byParent.set(key, [...(byParent.get(key) ?? []), g]);
  }
  for (const list of byParent.values()) list.sort((a, b) => a.name.localeCompare(b.name));
  const out: TreeRow<T>[] = [];
  const seen = new Set<string>();
  const walk = (key: string, depth: number) => {
    for (const g of byParent.get(key) ?? []) {
      if (seen.has(g.id)) continue;
      seen.add(g.id);
      out.push({ group: g, depth });
      walk(g.id, depth + 1);
    }
  };
  walk('', 0);
  return out;
}

/** descendantIds returns every group below id (a group cannot move there). */
export function descendantIds(groups: readonly TreeNode[], id: string): Set<string> {
  const out = new Set<string>();
  const visit = (parent: string) => {
    for (const g of groups) {
      if (g.parent_id === parent && !out.has(g.id)) {
        out.add(g.id);
        visit(g.id);
      }
    }
  };
  visit(id);
  return out;
}
