# Immutable release manifest

This tool validates and seals a release manifest using create-exclusive writes.
Every manifest records the source Git SHA, immutable OCI image digests, central
database migration version, configuration schema version, UTC build timestamp,
and the prior last-known-good release. It also emits a SHA-256 sidecar.

Mutable image tags such as `latest` are rejected. Existing manifests and their
checksums are never overwritten.

```sh
go run ./cmd/release-manifest -input release-input.json -output artifacts/release-2026-08-14.1.json
```
