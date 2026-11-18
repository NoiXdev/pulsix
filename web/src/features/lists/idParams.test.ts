import { describe, expect, it } from 'vitest';
import { validateActivitySearch } from '../activity/activitySearch.ts';
import { validateAlertsSearch } from '../alerts/alertsSearch.ts';
import { validateFleetSearch } from '../fleet/fleetSearch.ts';
import { validateRunsSearch } from '../runs/runsSearch.ts';
import { validateSchedulesSearch } from '../schedules/schedulesSearch.ts';

const ID = '3f2b8c1e-7a4d-4e6b-9c0a-1d2e3f4a5b6c';

// An id the server takes must be UUID-shaped, or the request answers 400 and
// the whole page (filter bar included) is replaced by the error page.
describe('id filters', () => {
  it.each([
    ['runs', validateRunsSearch, ['template', 'schedule', 'host']],
    ['alerts', validateAlertsSearch, ['host']],
    ['activity', validateActivitySearch, ['actor']],
    ['fleet', validateFleetSearch, ['zone', 'group']],
    ['schedules', validateSchedulesSearch, ['template']],
  ] as const)('%s keeps a UUID and drops anything else', (_name, validate, keys) => {
    for (const key of keys) {
      expect(validate({ [key]: ID })).toMatchObject({ [key]: ID });
      expect(validate({ [key]: 'x' })).toMatchObject({ [key]: undefined });
    }
  });
});
