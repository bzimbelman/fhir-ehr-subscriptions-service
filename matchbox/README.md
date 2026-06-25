# matchbox

Configuration for the Matchbox FHIR StructureMap engine. Matchbox loads the public HL7 v2-to-FHIR IG plus any project-specific FML maps and exposes `$transform` so the IPF app can convert HL7 v2 messages into FHIR Bundles.

## Layout

```
matchbox/
├── igs/                 ← IG packages (.tgz) mounted into the container at boot
│   └── (e.g. hl7.fhir.uv.v2mappings-X.Y.Z.tgz)
└── maps/                ← Project-owned FML files (custom Z-segments etc.)
```

IG packages are versioned in git so deployments are reproducible.
