import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { api, unwrap } from '../client.ts';
import type { NotifyChannelInput } from '../types.ts';

const ifMatch = (revision: number) => ({ 'If-Match': `"${revision}"` });

function useInvalidate(org: string) {
  const qc = useQueryClient();
  return () => {
    void qc.invalidateQueries({ queryKey: ['notification-channels', org] });
    void qc.invalidateQueries({ queryKey: ['notification-deliveries', org] });
  };
}

/** useChannels lists the org's notification channels plus whether the
 * instance can send e-mail at all (mail_available). */
export function useChannels(org: string, enabled = true) {
  return useQuery({
    queryKey: ['notification-channels', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/notification-channels', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export function useCreateChannel(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (body: NotifyChannelInput) => unwrap(api.POST('/api/v1/orgs/{org}/notification-channels', { params: { path: { org } }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useUpdateChannel(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: ({ revision, ...body }: NotifyChannelInput & { revision: number }) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/notification-channels/{id}', { params: { path: { org, id }, header: ifMatch(revision) }, body })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

export function useDeleteChannel(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (id: string) => unwrap(api.DELETE('/api/v1/orgs/{org}/notification-channels/{id}', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

/** testChannelKey is the mutation key of useTestChannel, so a page can read
 * which channels have a test in flight (useMutationState), one per channel,
 * rather than the one hook's latest call. */
export const testChannelKey = (org: string) => ['notification-channel-test', org];

/** useTestChannel sends the sample message through a channel right away;
 * the response's delivery says sent or failed (and why). */
export function useTestChannel(org: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationKey: testChannelKey(org),
    mutationFn: (id: string) => unwrap(api.POST('/api/v1/orgs/{org}/notification-channels/{id}/test', { params: { path: { org, id } } })),
    onSuccess: invalidate,
  });
}

export function useChannelDeliveries(org: string, id: string) {
  return useQuery({
    queryKey: ['notification-deliveries', org, id],
    queryFn: () =>
      unwrap(api.GET('/api/v1/orgs/{org}/notification-channels/{id}/deliveries', { params: { path: { org, id }, query: { limit: 50 } } })),
    enabled: !!org && !!id,
  });
}

/** useDeliveryPayload reads one delivery with the event that was sent
 * (backlog 2.75). Separate from the log's list, which returns up to a
 * hundred rows and leaves the payloads out; this is the one an operator
 * actually opened. */
export function useDeliveryPayload(org: string, id: string, delivery: string | null) {
  return useQuery({
    queryKey: ['notification-delivery', org, id, delivery],
    queryFn: () =>
      unwrap(api.GET('/api/v1/orgs/{org}/notification-channels/{id}/deliveries/{delivery}', {
        params: { path: { org, id, delivery: delivery as string } },
      })),
    enabled: !!org && !!id && !!delivery,
  });
}

/** useResendDelivery puts a failed delivery back on the queue (backlog
 * 2.75), so a message lost to a receiver that was down can go out once
 * it is back — without making the underlying event happen again. */
export function useResendDelivery(org: string, id: string) {
  const invalidate = useInvalidate(org);
  return useMutation({
    mutationFn: (delivery: string) =>
      unwrap(api.POST('/api/v1/orgs/{org}/notification-channels/{id}/deliveries/{delivery}/resend', {
        params: { path: { org, id, delivery } },
      })),
    onSuccess: invalidate,
    gcTime: 0,
  });
}

/** useScheduleNotifications reads a schedule's notification rules in the
 * API's {on, channels} shape. */
export function useScheduleNotifications(org: string, scheduleId: string) {
  return useQuery({
    queryKey: ['schedule-notifications', org, scheduleId],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/schedules/{id}/notifications', { params: { path: { org, id: scheduleId } } })),
    enabled: !!org && !!scheduleId,
  });
}

export type NotifyRuleInput = { on: 'failure' | 'change' | 'success' | 'skipped'; channels: string[] };

/** useSetScheduleNotifications replaces a schedule's rules; the server
 * merges them per channel, so a run yields at most one message each. */
export function useSetScheduleNotifications(org: string, scheduleId: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (rules: NotifyRuleInput[]) =>
      unwrap(api.PUT('/api/v1/orgs/{org}/schedules/{id}/notifications', { params: { path: { org, id: scheduleId } }, body: { rules } })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['schedule-notifications', org, scheduleId] }),
  });
}

export function useDigest(org: string, enabled = true) {
  return useQuery({
    queryKey: ['notification-digest', org],
    queryFn: () => unwrap(api.GET('/api/v1/orgs/{org}/notification-digest', { params: { path: { org } } })),
    enabled: !!org && enabled,
  });
}

export type DigestInput = { enabled: boolean; weekday: 'sun' | 'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat'; time: string; timezone: string; channels: string[] };

export function useSetDigest(org: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (body: DigestInput) => unwrap(api.PUT('/api/v1/orgs/{org}/notification-digest', { params: { path: { org } }, body })),
    onSuccess: () => void qc.invalidateQueries({ queryKey: ['notification-digest', org] }),
  });
}
