import { Link } from "@tanstack/react-router";
import { Copy } from "lucide-react";
import { Fragment, type ReactNode } from "react";
import { useT } from "../../i18n/useT.ts";
import { useCopy } from "../../ui/useCopy.ts";
import {
  addCommand,
  checkCommand,
  downloadCommand,
  suggestedName,
} from "./assistantConnect.ts";

function CommandBlock({ command, what }: { command: string; what: string }) {
  const t = useT();
  const copy = useCopy();
  return (
    <div className="flex items-start gap-2 rounded-md border border-border bg-background">
      <code className="min-w-0 flex-1 overflow-x-auto whitespace-pre p-2.5 font-mono text-xs">
        {command}
      </code>
      <button
        type="button"
        aria-label={t.tokens.connect.copyCommand(what)}
        title={t.tokens.connect.copyCommand(what)}
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
  settingsOrgs = [],
}: {
  origin: string;
  orgSlugs: readonly string[];
  personal: boolean;
  showSecretStep: boolean;
  /** The organizations among `orgSlugs` whose Settings → General the viewer may change. */
  settingsOrgs?: readonly string[];
}) {
  const g = useT().tokens.connect;
  const servers = orgSlugs.map((slug) => ({
    slug,
    name: suggestedName(origin, slug),
  }));
  const links = orgSlugs.filter((slug) => settingsOrgs.includes(slug));
  let httpsOrigin = true;
  try {
    httpsOrigin = new URL(origin).protocol === "https:";
  } catch {
    // An unparsable origin cannot be checked; show the commands.
  }
  const body = !httpsOrigin ? (
    <p className="text-xs">{g.httpOnly}</p>
  ) : servers.length === 0 ? (
    <p className="text-xs">{g.noOrg}</p>
  ) : null;
  return (
    <section aria-label={g.title} className="flex flex-col gap-3">
      <h3 className="text-xs font-semibold">{g.title}</h3>
      {body ?? (
        <ol className="flex list-decimal flex-col gap-3 pl-5">
          <Step title={g.download}>
            <CommandBlock
              command={downloadCommand(origin)}
              what={g.cmdDownload}
            />
          </Step>
          <Step title={personal ? g.registerPersonal : g.register}>
            {servers.map((s) => (
              <CommandBlock
                key={s.slug}
                command={addCommand(
                  origin,
                  s.name,
                  personal ? s.slug : undefined,
                )}
                what={g.cmdRegister(s.name)}
              />
            ))}
          </Step>
          <Step title={showSecretStep ? g.pasteHere : g.pasteSaved} />
          <Step title={g.check}>
            {servers.map((s) => (
              <CommandBlock
                key={s.slug}
                command={checkCommand(s.name)}
                what={g.cmdCheck(s.name)}
              />
            ))}
          </Step>
        </ol>
      )}
      <p className="text-[11px] text-muted-foreground">
        {g.noteStart}
        {links.length === 0
          ? g.settingsGeneral
          : links.map((slug, i) => (
              <Fragment key={slug}>
                {i > 0 && ", "}
                <Link
                  to="/o/$org/settings/general"
                  params={{ org: slug }}
                  className="underline"
                >
                  {links.length > 1
                    ? `${g.settingsGeneral} (${slug})`
                    : g.settingsGeneral}
                </Link>
              </Fragment>
            ))}
        {g.noteEnd}
      </p>
    </section>
  );
}
