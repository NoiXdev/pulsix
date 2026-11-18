import { useHostInventoryPreview } from '../../api/hooks/useInventory.ts';
import { useBastions } from '../../api/hooks/useSsh.ts';
import type { Host } from '../../api/types.ts';

/** How a managed host is reached over SSH: where the route comes from
 * (`bastion_source`, see routeLabel) and the jump hosts on it, by name. */
export interface HostRoute {
  source: string;
  names: string[];
}

/**
 * useHostRoute resolves a managed host's SSH route from its inventory
 * preview, naming each jump host (a piece of its id while the jump hosts
 * are not known). Undefined for a host that is not managed — only a managed
 * host is reached over SSH — and until the preview answers.
 */
export function useHostRoute(org: string, host: Pick<Host, 'id' | 'managed'>): HostRoute | undefined {
  const preview = useHostInventoryPreview(org, host.managed ? host.id : '');
  const bastions = useBastions(org);
  const connection = preview.data?.connection;
  if (!host.managed || !connection) return undefined;
  const name = (id: string) => bastions.data?.bastions?.find((b) => b.id === id)?.name ?? id.slice(0, 8);
  return { source: connection.bastion_source, names: (connection.bastions ?? []).map(name) };
}
