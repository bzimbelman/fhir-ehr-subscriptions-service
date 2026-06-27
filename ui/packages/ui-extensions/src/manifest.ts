import type { Extension } from "./extensionPoints";

/**
 * Sealed enumeration of who authored an extension. The host shows
 * the supplier in the footer indicator and uses it to decide what
 * trust banner to render — `community` supplier is currently
 * blocked at the registry level (see master plan §3.2.1) and exists
 * here for forward compatibility.
 */
export type ExtensionSupplier = "first-party" | "commercial" | "community";

/**
 * Declarative description of a UI extension bundle. Every extension
 * package — FOSS, commercial, or community — exports one of these
 * and the host's {@link UiExtensionRegistry} registers it at boot.
 *
 * - `schemaVersion` must equal {@link SPI_SCHEMA_VERSION}; the
 *   registry refuses unknown versions.
 * - `id` is the package id (e.g. `"compliance-iti20"`), not to be
 *   confused with the per-extension `id` inside `extensions`.
 * - `version` is the manifest's own semver, useful for telemetry.
 * - `extensions` is the list of registered extension points.
 */
export interface UiExtensionManifest {
  readonly schemaVersion: number;
  readonly id: string;
  readonly version: string;
  readonly supplier: ExtensionSupplier;
  readonly extensions: readonly Extension[];
}
