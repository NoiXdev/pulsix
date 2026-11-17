import { createContext, useContext } from 'react';

export interface LockState {
  locked: boolean;
  setLocked: (locked: boolean) => void;
}

/** LockStateContext is provided by routes/RootLayout.tsx, which owns the
 * lock state. Outside it (a test rendering one page) nothing is locked and
 * locking is a no-op. */
export const LockStateContext = createContext<LockState>({ locked: false, setLocked: () => {} });

export function useLockState(): LockState {
  return useContext(LockStateContext);
}
