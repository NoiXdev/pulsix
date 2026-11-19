import { useEffect, useRef, useState } from 'react';
import { useNavigate, useParams, useSearch } from '@tanstack/react-router';
import { PageTabs, type PageTabItem } from '../../ui/page-tabs.tsx';
import { QueryLoading } from '../../ui/QueryState.tsx';
import { useQueryGuard } from '../../ui/useQueryGuard.tsx';
import { useToast } from '../../ui/toastContext.ts';
import { useUnsavedGuard } from '../../ui/useUnsavedGuard.ts';
import { useOrg } from '../../api/hooks/useOrgs.ts';
import { usePlaybook, useUpdatePlaybook } from '../../api/hooks/usePlaybooks.ts';
import { useTemplates } from '../../api/hooks/useTemplates.ts';
import type { Playbook } from '../../api/types.ts';
import { may } from '../../lib/roles.ts';
import { useT } from '../../i18n/useT.ts';
import { PlaybookEditorTab } from './PlaybookEditorTab.tsx';
import { PlaybookHeader } from './PlaybookHeader.tsx';
import { PlaybookRunsTab } from './PlaybookRunsTab.tsx';
import { PlaybookUsedByTab } from './PlaybookUsedByTab.tsx';
import { parsePlaybookTab, playbookPanelId, playbookTabId, type PlaybookTab } from './playbookTabs.ts';
import { usePlaybookDraft } from './usePlaybookDraft.ts';
import { validateYAMLProblem } from './validate.ts';

/**
 * PlaybookPage is one playbook on a page of its own (spec
 * 2026-11-19-playbook-pages-design.md §1): a header with its name, state and
 * actions, and the tabs Editor, Runs and Used by, the open one in the URL
 * (`?tab=`, Editor when absent).
 *
 * The draft lives here, above the tabs (usePlaybookDraft: a clean draft
 * follows the stored text), and the Editor panel stays mounted
 * (hidden) while another tab is open, so a tab switch keeps unsaved text and
 * the editor's own undo history; leaving the page with it asks first
 * (useUnsavedGuard), a tab switch does not.
 */
export function PlaybookPage() {
  const { org, id } = useParams({ strict: false }) as { org: string; id: string };
  const orgInfo = useOrg(org);
  const detail = usePlaybook(org, id);

  // The role decides what the page offers, so the org is guarded first.
  const orgGuard = useQueryGuard(orgInfo);
  const guard = useQueryGuard(detail);
  if (orgGuard) return orgGuard;
  if (guard) return guard;
  if (!orgInfo.data || !detail.data) return <QueryLoading />;
  // Keyed by the playbook: a draft never follows the person to another one.
  return <PlaybookDetail key={id} org={org} playbook={detail.data.playbook} role={orgInfo.data.org.role} />;
}

const PANEL_CLASS = 'flex min-w-0 flex-col gap-4';

/** A dialog open in the document, modal or not: the page's shortcuts are
 * not its. */
const OPEN_DIALOG = 'dialog[open], [role="dialog"], [role="alertdialog"], [aria-modal="true"]';

function PlaybookDetail({ org, playbook, role }: { org: string; playbook: Playbook; role: string }) {
  const t = useT();
  const toast = useToast();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { tab?: unknown };
  const tab = parsePlaybookTab(search.tab);
  const templates = useTemplates(org);
  const update = useUpdatePlaybook(org, playbook.id);
  const editable = playbook.source === 'editor' && may(role, 'playbooks.write');

  const { saved, draft, setDraft, markSaved } = usePlaybookDraft(playbook.inline ?? '');
  const [serverError, setServerError] = useState<string | null>(null);
  const dirty = editable && draft !== saved;
  const release = useUnsavedGuard(dirty, { samePath: true });
  const problem = playbook.source === 'editor' ? validateYAMLProblem(draft, t) : null;
  const canSave = editable && dirty && problem === null && !update.isPending;

  const using = (templates.data?.templates ?? []).filter((tpl) => tpl.playbook_id === playbook.id);

  async function save() {
    if (!canSave) return;
    const sent = draft;
    setServerError(null);
    try {
      await update.mutateAsync({ inline: sent });
      markSaved(sent);
      toast.show(t.playbooks.editor.saved);
    } catch (err) {
      setServerError(err instanceof Error ? err.message : t.playbooks.editor.saveFailed);
    }
  }

  // Ctrl/Cmd-S saves from anywhere on the Editor tab of an editable
  // playbook (the header, the side column, the editor), and never opens
  // the browser's "Save page" there; save() itself refuses a clean,
  // invalid or pending draft. A key something else already took, or one
  // pressed while a dialog is open (Run…, the delete confirmation, any
  // other), is not the page's.
  const saveRef = useRef(save);
  useEffect(() => {
    saveRef.current = save;
  });
  const shortcut = tab === 'editor' && editable;
  useEffect(() => {
    if (!shortcut) return;
    const onKeyDown = (ev: KeyboardEvent) => {
      if (ev.defaultPrevented || !(ev.ctrlKey || ev.metaKey) || ev.altKey || ev.key.toLowerCase() !== 's') return;
      if (document.querySelector(OPEN_DIALOG)) return;
      ev.preventDefault();
      void saveRef.current();
    };
    document.addEventListener('keydown', onKeyDown);
    return () => document.removeEventListener('keydown', onKeyDown);
  }, [shortcut]);

  // Arrow/Home/End roving replaces the history entry.
  const select = (next: PlaybookTab, via: 'click' | 'key' = 'click') =>
    void navigate({ to: '/o/$org/playbooks/$id', params: { org, id: playbook.id }, search: { tab: next === 'editor' ? undefined : next }, replace: via === 'key' });

  const tabs = t.playbooks.page.tabs;
  const items: PageTabItem<PlaybookTab>[] = [
    { id: 'editor', label: tabs.editor },
    { id: 'runs', label: tabs.runs },
    { id: 'used', label: tabs.used, count: templates.data ? using.length : undefined },
  ];

  return (
    <div className="flex flex-col gap-5">
      <PlaybookHeader
        org={org}
        playbook={playbook}
        role={role}
        templates={templates.data ? using : undefined}
        templatesFailed={templates.isError}
        problem={playbook.source === 'editor' ? problem : null}
        dirty={dirty}
        canSave={canSave}
        onSave={() => void save()}
        onLeave={release}
      />
      <PageTabs label={t.playbooks.page.tabsLabel} tabs={items} active={tab} onSelect={select} tabId={playbookTabId} panelId={playbookPanelId} />
      <div
        role="tabpanel"
        id={playbookPanelId('editor')}
        aria-labelledby={playbookTabId('editor')}
        hidden={tab !== 'editor'}
        className={tab === 'editor' ? PANEL_CLASS : 'hidden'}
      >
        <PlaybookEditorTab
          org={org}
          playbook={playbook}
          canWrite={may(role, 'playbooks.write')}
          draft={draft}
          onChange={setDraft}
          problem={problem}
          serverError={serverError}
          templates={using}
          onShowRuns={() => select('runs')}
        />
      </div>
      {tab !== 'editor' && (
        <div role="tabpanel" id={playbookPanelId(tab)} aria-labelledby={playbookTabId(tab)} className={PANEL_CLASS}>
          {tab === 'runs' ? (
            <PlaybookRunsTab org={org} playbookId={playbook.id} templates={using} canRun={may(role, 'jobs.run')} />
          ) : (
            <PlaybookUsedByTab org={org} role={role} playbookId={playbook.id} templates={templates.data ? using : undefined} error={templates.isError ? templates.error : null} />
          )}
        </div>
      )}
    </div>
  );
}
