import { useQuery } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

/** useResetAvailable reads the public flag that says whether a password can
 * be reset by e-mail: outbound mail is configured. */
export function useResetAvailable() {
  return useQuery({
    queryKey: ['password-reset-available'],
    queryFn: () => unwrap(api.GET('/api/v1/auth/password/reset-available')),
  });
}
