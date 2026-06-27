/**
 * Schema version of the UI extension SPI.
 *
 * The host UI rejects any {@link UiExtensionManifest} whose
 * `schemaVersion` is not equal to this constant. Bumping the version
 * is a breaking change — every published manifest must be re-emitted
 * against the new schema. We treat this as the source of truth for
 * "did the SPI change shape?"; the package's npm version tracks
 * additive changes too, but the runtime check is on this number.
 */
export const SPI_SCHEMA_VERSION = 1 as const;

export type SpiSchemaVersion = typeof SPI_SCHEMA_VERSION;
