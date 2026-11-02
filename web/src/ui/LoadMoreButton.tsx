import { useT } from '../i18n/useT.ts';
import { Button } from './button.tsx';

/**
 * LoadMoreButton is the shared "fetch the next page" control under a
 * cursor-paginated list (the hosts list, the activity feed). The caller
 * renders it only while there is a next page; `loading` disables it
 * while that page is on its way, so a double click cannot queue the same
 * page twice.
 */
export function LoadMoreButton({ onClick, loading }: { onClick: () => void; loading: boolean }) {
  const t = useT();
  return (
    <div className="flex justify-center">
      <Button type="button" onClick={onClick} disabled={loading}>
        {loading ? t.common.loading : t.ui.loadMore}
      </Button>
    </div>
  );
}
