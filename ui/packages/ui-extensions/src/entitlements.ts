/**
 * An entitlement is a dotted-namespace string identifying a paid
 * feature: `"audit.export.iti20"`, `"alerting.pagerduty"`, etc. The
 * host's license-check client returns the set of entitlements the
 * current license grants; the {@link UiExtensionRegistry} returns
 * only extensions whose `requiredEntitlement` is in that set (or is
 * omitted, meaning "FOSS — always shown").
 *
 * The brand keeps callers from accidentally passing arbitrary
 * strings where an Entitlement is expected — you must funnel through
 * {@link entitlement} to mint one, which makes "where did this
 * entitlement come from?" greppable.
 */
export type Entitlement = string & { readonly __brand: "Entitlement" };

/**
 * Mint an {@link Entitlement} from a raw string. No validation
 * beyond the brand — we don't enforce dot-separated namespaces here
 * because the host owns the canonical list. Callers in tests and in
 * the license-check client both use this.
 */
export function entitlement(value: string): Entitlement {
  return value as Entitlement;
}

/**
 * Read-only view of the entitlements the current license grants.
 * Construct with {@link makeEntitlementSet}. Membership is by string
 * identity (Entitlement is a branded string), so two entitlements
 * with the same underlying value are equal.
 */
export interface EntitlementSet {
  has(e: Entitlement): boolean;
  toArray(): readonly Entitlement[];
}

/**
 * Build an {@link EntitlementSet} from raw strings (e.g. the array
 * returned by the license server). Duplicates are collapsed.
 */
export function makeEntitlementSet(values: readonly string[]): EntitlementSet {
  const inner = new Set<string>(values);
  return {
    has(e: Entitlement): boolean {
      return inner.has(e as unknown as string);
    },
    toArray(): readonly Entitlement[] {
      return Array.from(inner, (v) => v as Entitlement);
    },
  };
}

/**
 * The empty entitlement set — useful for FOSS-only callers and for
 * tests. Returns the same object every call; it's immutable.
 */
export const EMPTY_ENTITLEMENT_SET: EntitlementSet = makeEntitlementSet([]);
