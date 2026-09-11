import { useMutation, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';

export function useEnrolTotp() {
  return useMutation({
    mutationFn: (code?: string) => unwrap(api.POST('/api/v1/auth/totp/enrol', { body: { code: code ?? '' } })),
  });
}

export function useConfirmTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => unwrap(api.POST('/api/v1/auth/totp/confirm', { body: { code } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}

export function useDisableTotp() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (code: string) => unwrap(api.DELETE('/api/v1/auth/totp', { body: { code } })),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['me'] }),
  });
}
