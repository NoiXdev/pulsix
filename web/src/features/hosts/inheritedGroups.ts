interface GroupNode {
  id: string;
  name: string;
  parent_id?: string | null;
}

/** inheritedGroups lists the groups a host is in only through a subgroup:
 * the ancestors of its direct groups that are not direct themselves, each
 * with the (first) direct group it comes through. */
export function inheritedGroups(all: readonly GroupNode[], direct: readonly { id: string; name: string }[]): { id: string; name: string; via: string }[] {
  const byId = new Map(all.map((g) => [g.id, g]));
  const isDirect = new Set(direct.map((g) => g.id));
  const seen = new Map<string, { id: string; name: string; via: string }>();
  for (const d of direct) {
    const visited = new Set<string>([d.id]);
    let parent = byId.get(d.id)?.parent_id;
    while (parent && !visited.has(parent)) {
      visited.add(parent);
      const node = byId.get(parent);
      if (!node) break;
      if (!isDirect.has(node.id) && !seen.has(node.id)) seen.set(node.id, { id: node.id, name: node.name, via: d.name });
      parent = node.parent_id;
    }
  }
  return [...seen.values()].sort((a, b) => a.name.localeCompare(b.name));
}
