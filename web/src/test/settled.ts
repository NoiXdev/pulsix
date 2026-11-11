import { act } from '@testing-library/react';

/** Lets a pending navigation or the close event of a confirmation dialog land,
 * so an assertion that something did NOT happen cannot pass by being early. */
export async function settled(): Promise<void> {
  await act(async () => {
    await new Promise((r) => setTimeout(r, 20));
  });
}
