import { useEffect, useState } from 'react';
import { useVersionWatch } from '../../api/hooks/useSetupStatus.ts';
import { baseline } from './baseline.ts';
import { VersionBanner } from './VersionBanner.tsx';
import { VersionDialog } from './VersionDialog.tsx';

const LATER_KEY = 'pulsix.version.later';

function deferred(): string | null {
  try {
    return sessionStorage.getItem(LATER_KEY);
  } catch {
    return null;
  }
}

function defer(version: string) {
  try {
    sessionStorage.setItem(LATER_KEY, version);
  } catch {
    /* storage may be blocked; the in-memory state still holds */
  }
}

/**
 * VersionWatcher (backlog 3.94) notices that the manager was upgraded while
 * this tab stayed open. It remembers the first version it reads; a later
 * successful read of a different one offers a reload. Failed reads change
 * nothing. "Later" turns the dialog into a banner and remembers the version
 * so the dialog does not come back.
 */
export function VersionWatcher() {
  const { data } = useVersionWatch();
  const version = data?.version;
  const [later, setLater] = useState<string | null>(null);

  // The first successful read's version, taken once per page load: held in
  // state (set during render, React's adjust-on-change pattern) and copied to
  // the module store so a remount after the lock screen keeps it.
  const [known, setKnown] = useState<string | null>(baseline.value);
  if (version && known === null) setKnown(version);
  useEffect(() => {
    if (known !== null && baseline.value === null) baseline.value = known;
  }, [known]);
  const changed = version && known !== null && version !== known ? version : null;

  if (!changed) return null;
  const reload = () => window.location.reload();
  if (later === changed || deferred() === changed) return <VersionBanner version={changed} onReload={reload} />;
  return (
    <VersionDialog
      version={changed}
      onReload={reload}
      onLater={() => {
        defer(changed);
        setLater(changed);
      }}
    />
  );
}
