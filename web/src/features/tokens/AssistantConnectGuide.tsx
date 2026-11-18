import { Copy } from 'lucide-react';
import type { ReactNode } from 'react';
import { useT } from '../../i18n/useT.ts';
import { useCopy } from '../../ui/useCopy.ts';
import { addCommand, checkCommand, downloadCommand, suggestedName } from './assistantConnect.ts';

function CommandBlock({ command }: { command: string }) {
  const t = useT();
  const copy = useCopy();
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-background">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre p-2.5 font-mono text-xs">{command}</code>
      <button
        type="button"
        aria-label={t.tokens.connect.copyCommand}
        title={t.tokens.connect.copyCommand}
        onClick={() => void copy(command)}
        className="m-1 shrink-0 rounded p-1.5 text-muted-foreground hover:bg-muted hover:text-foreground"
      >
        <Copy size={14} aria-hidden="true" />
      </button>
    </div>
  );
}

function Step({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <li className="flex flex-col gap-1.5">
      <span className="text-xs">{title}</span>
      {children}
    </li>
  );
}

/**
 * AssistantConnectGuide is the click guide to connect an assistant to the
 * manager: download the script, register the server, paste the token, check.
 * `orgSlugs` are the organizations the token works in (one for an
 * organization token); a personal token registers one server per slug.
 * `showSecretStep` is true under a just-created token, whose Copy button the
 * third step points at.
 */
export function AssistantConnectGuide({
  origin,
  orgSlugs,
  personal,
  showSecretStep,
}: {
  origin: string;
  orgSlugs: readonly string[];
  personal: boolean;
  showSecretStep: boolean;
}) {
  const g = useT().tokens.connect;
  const servers = orgSlugs.map((slug) => ({ slug, name: suggestedName(origin, slug) }));
  return (
    <section aria-label={g.title} className="flex flex-col gap-3">
      <h3 className="text-xs font-semibold">{g.title}</h3>
      <ol className="flex list-decimal flex-col gap-3 pl-5">
        <Step title={g.download}>
          <CommandBlock command={downloadCommand(origin)} />
        </Step>
        <Step title={personal ? g.registerPersonal : g.register}>
          {servers.map((s) => (
            <CommandBlock key={s.slug} command={addCommand(origin, s.name, personal ? s.slug : undefined)} />
          ))}
        </Step>
        <Step title={showSecretStep ? g.pasteHere : g.pasteSaved} />
        <Step title={g.check}>
          {servers.map((s) => (
            <CommandBlock key={s.slug} command={checkCommand(s.name)} />
          ))}
        </Step>
      </ol>
      <p className="text-[11px] text-muted-foreground">{g.note}</p>
    </section>
  );
}
