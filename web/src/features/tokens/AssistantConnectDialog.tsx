import { Button } from '../../ui/button.tsx';
import { Dialog } from '../../ui/dialog.tsx';
import { useT } from '../../i18n/useT.ts';
import { AssistantConnectGuide } from './AssistantConnectGuide.tsx';

/**
 * AssistantConnectDialog shows the connect guide for an existing assistant
 * token (row menu "Connect an assistant…"), without the shown-once secret.
 */
export function AssistantConnectDialog({
  name,
  orgSlugs,
  personal,
  onClose,
}: {
  name: string;
  orgSlugs: readonly string[];
  personal: boolean;
  onClose: () => void;
}) {
  const t = useT();
  return (
    <Dialog open onClose={onClose} title={t.tokens.connect.dialogTitle(name)} size="wide">
      <div className="flex flex-col gap-4">
        <AssistantConnectGuide origin={window.location.origin} orgSlugs={orgSlugs} personal={personal} showSecretStep={false} />
        <div className="flex justify-end">
          <Button variant="primary" onClick={onClose}>
            {t.tokens.done}
          </Button>
        </div>
      </div>
    </Dialog>
  );
}
