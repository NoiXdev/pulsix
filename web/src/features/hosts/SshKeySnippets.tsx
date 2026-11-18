import { useState } from 'react';
import { useCredential } from '../../api/hooks/useCredentials.ts';
import type { Credential } from '../../api/types.ts';
import { useT } from '../../i18n/useT.ts';
import { PublicKeyPanel } from '../credentials/PublicKeyPanel.tsx';

/**
 * SshKeySnippets is the SSH access dialog's collapsed "Install this key on
 * the host": the public key of the chosen SSH key credential with the
 * authorized_keys, cloud-init and Terraform snippets. The credential's
 * detail (which carries them) is read only once the section is open.
 */
export function SshKeySnippets({ org, credential, defaultOpen = false }: { org: string; credential?: Credential; defaultOpen?: boolean }) {
  const t = useT();
  const [open, setOpen] = useState(defaultOpen);
  const keyId = credential?.kind === 'ssh_key' ? credential.id : '';
  const detail = useCredential(org, open ? keyId : '');
  const publicKey = detail.data?.credential.public_key;

  let body;
  if (publicKey) body = <PublicKeyPanel publicKey={publicKey} snippets={detail.data?.snippets} />;
  else if (keyId && detail.isPending) body = <p className="text-xs text-muted-foreground">{t.common.loading}</p>;
  else body = <p className="text-xs text-muted-foreground">{t.hosts.sshDialog.snippetsNone}</p>;

  return (
    <details open={open} onToggle={(e) => setOpen(e.currentTarget.open)} className="rounded-md border border-border">
      <summary className="cursor-pointer px-3 py-2 text-xs font-semibold">{t.hosts.ssh.installKey}</summary>
      {open && <div className="border-t border-border p-3">{body}</div>}
    </details>
  );
}
