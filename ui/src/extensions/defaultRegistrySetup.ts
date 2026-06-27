/**
 * One-shot wiring that seeds the process-wide default registry with
 * the FOSS builtin manifest. The root layout invokes this once on
 * import; idempotency makes repeated calls safe.
 *
 * Lives in its own module so the cycle-creating import sits in a
 * single, isolated place -- `UiExtensionRegistry.ts` knows nothing
 * about `builtinManifests`, and `useExtensions.ts` doesn't drag the
 * App Router page imports into the test bundle.
 */

import { builtinManifest } from "./builtinManifests";
import { getDefaultRegistry } from "./UiExtensionRegistry";

let _seeded = false;

/**
 * Register the FOSS builtin manifest in the default registry. Safe
 * to call multiple times -- subsequent calls are a no-op.
 */
export function seedDefaultRegistryWithBuiltins(): void {
  if (_seeded) return;
  getDefaultRegistry().register(builtinManifest);
  _seeded = true;
}

/**
 * Reset the seeding flag. ONLY for tests.
 */
export function __resetSeededForTests(): void {
  _seeded = false;
}
