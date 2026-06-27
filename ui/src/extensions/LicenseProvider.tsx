"use client";

/**
 * Client-side context that holds the current `LicenseState`. The
 * `useExtensions` hook reads this to filter the registry by
 * entitlements.
 *
 * The state is loaded SERVER-SIDE (the license client uses
 * `node:crypto` and writes to disk) and passed in as `initialState`
 * from the root layout. The provider then optionally schedules
 * periodic refreshes via a server action; for ticket #437 we
 * deliberately keep the provider passive -- it just holds the
 * initial value. Refresh-on-demand is wired up in Epic #428.
 */

import {
  createContext,
  useCallback,
  useContext,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import type { LicenseState } from "@/lib/license/types";

const DEFAULT_FOSS_STATE: LicenseState = {
  kind: "foss",
  reason: "no-license-key",
};

interface LicenseContextValue {
  readonly state: LicenseState;
  /**
   * Replace the state. Provided so future tickets (and tests) can
   * drive transitions without rewiring the provider.
   */
  setState: (next: LicenseState) => void;
}

const LicenseContext = createContext<LicenseContextValue>({
  state: DEFAULT_FOSS_STATE,
  setState: () => {
    /* no-op default; overridden by the provider */
  },
});

export interface LicenseProviderProps {
  readonly initialState?: LicenseState;
  readonly children: ReactNode;
}

/**
 * Wrap the operator UI in this provider at the root layout so every
 * descendant can call `useLicenseState()` and `useExtensions()`. The
 * server-rendered layout passes the boot-time `LicenseState` through
 * `initialState`.
 */
export function LicenseProvider({
  initialState,
  children,
}: LicenseProviderProps): React.ReactElement {
  const [state, setState] = useState<LicenseState>(
    initialState ?? DEFAULT_FOSS_STATE,
  );

  const setStateStable = useCallback((next: LicenseState) => {
    setState(next);
  }, []);

  const value = useMemo<LicenseContextValue>(
    () => ({ state, setState: setStateStable }),
    [state, setStateStable],
  );

  return (
    <LicenseContext.Provider value={value}>{children}</LicenseContext.Provider>
  );
}

/**
 * Read the current license state. Returns the FOSS default if no
 * provider is mounted, so server components and isolated unit tests
 * that don't wrap the tree still get a sensible value.
 */
export function useLicenseState(): LicenseState {
  return useContext(LicenseContext).state;
}

/**
 * Get a setter for the license state. Tests use this to drive
 * transitions; production code does not need it today.
 */
export function useSetLicenseState(): (next: LicenseState) => void {
  return useContext(LicenseContext).setState;
}
