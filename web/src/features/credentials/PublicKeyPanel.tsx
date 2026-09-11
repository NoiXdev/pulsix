import { useState } from 'react';
import { Copy } from 'lucide-react';
import { Button } from '../../ui/button.tsx';

interface Snippets {
  authorized_keys_command: string;
  cloud_init: string;
  terraform: string;
}

const SNIPPETS: Array<[keyof Snippets, string]> = [
  ['authorized_keys_command', 'authorized_keys'],
  ['cloud_init', 'cloud-init'],
  ['terraform', 'Terraform'],
];

/** PublicKeyPanel shows the key an operator installs on a host, with the
 * spec §5 snippets. The private key never reaches the browser. */
export function PublicKeyPanel({ publicKey, snippets }: { publicKey: string; snippets?: Snippets | null }) {
  const [tab, setTab] = useState<keyof Snippets>('authorized_keys_command');
  return (
    <div className="flex flex-col gap-3">
      <CopyBlock label="Public key" text={publicKey} />
      {snippets && (
        <div className="flex flex-col gap-2">
          <div role="tablist" className="flex gap-4 border-b border-border">
            {SNIPPETS.map(([key, label]) => (
              <button
                key={key}
                type="button"
                role="tab"
                aria-selected={tab === key}
                className={`border-b-2 py-1.5 text-xs ${tab === key ? 'border-primary font-semibold' : 'border-transparent text-muted-foreground'}`}
                onClick={() => setTab(key)}
              >
                {label}
              </button>
            ))}
          </div>
          <CopyBlock label={SNIPPETS.find(([k]) => k === tab)?.[1] ?? ''} text={snippets[tab]} />
        </div>
      )}
    </div>
  );
}

function CopyBlock({ label, text }: { label: string; text: string }) {
  return (
    <div className="flex flex-col gap-1">
      <div className="flex items-center justify-between">
        <span className="text-[11px] font-medium text-muted-foreground">{label}</span>
        <Button type="button" variant="ghost" aria-label={`Copy ${label}`} onClick={() => void navigator.clipboard.writeText(text)}>
          <Copy size={14} /> Copy
        </Button>
      </div>
      <pre aria-label={label} className="overflow-x-auto rounded-md border border-border bg-background p-3 font-mono text-[11px] whitespace-pre-wrap break-all">
        {text}
      </pre>
    </div>
  );
}
