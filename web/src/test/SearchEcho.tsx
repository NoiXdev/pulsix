import { useSearch } from '@tanstack/react-router';

/** SearchEcho stands in for a page a test navigates to and prints the
 * `?host=` it was opened with, so a test can tell a link that carries the
 * filter from one that only points at the right path. */
export function SearchEcho({ name }: { name: string }) {
  const search = useSearch({ strict: false }) as { host?: string };
  return (
    <div>
      {name} host={search.host ?? '(none)'}
    </div>
  );
}
