import type { Bastion } from '../../api/types.ts';

/** The state of a jump host's key: what the newest connection test saw, or
 * "pinned" when a key is stored, else ''. The host key cell and the row
 * menu's Pin and Unpin read it. */
export function bastionKeyState(b: Bastion): string {
  return b.observed_state ?? (b.ssh_host_key ? 'pinned' : '');
}
