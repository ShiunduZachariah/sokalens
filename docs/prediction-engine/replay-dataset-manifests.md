# Replay dataset manifests

Date: 2026-08-14

ENG-007 introduces `replay-dataset-manifest/v1`, an immutable and
self-verifying contract for point-in-time development and final evaluation.
It does not build dataset extracts or run a model. It records exactly which
prepared data a future replay or champion/challenger cycle is allowed to use.

Each manifest contains four half-open, UTC-normalized chronological windows:

1. `train` fits model parameters.
2. `test` is the tuning/validation fold used for hyperparameters, ensemble and
   recommendation thresholds. It is not the final headline test.
3. `calibration` fits calibration only and is out of sample from model fitting.
4. `final_holdout` is reserved for the one-shot pre-launch or champion versus
   challenger headline evaluation.

Windows may be adjacent or have deliberate gaps, but they cannot overlap or
move backwards. Every boundary and the manifest `as_of_timestamp` must be
timezone-aware. No partition may extend beyond that as-of timestamp.
The manifest creation timestamp must be at or after the as-of cutoff, so a
manifest cannot claim to attest to data from its own future.

The manifest records stable dataset, source-snapshot and materialized-artifact
identifiers, record counts, content SHA-256 values, config hash, feature
version, code commit and creation/as-of timestamps. Identifier collections
must be sorted and unique. Canonical JSON normalizes timestamps to UTC, sorts
object keys, uses fixed separators and ends with one newline. The envelope
stores the SHA-256 of the canonical manifest payload; parsing rejects unknown
fields, unsupported schema versions and hash mismatches.

The development interface exposes only train, tuning-test and calibration.
The final holdout is available only from `final_evaluation()`, and runtime role
checks prevent constructing either interface with the wrong partition. This
keeps holdout data out of fitting, feature selection, hyperparameter tuning,
ensemble weighting, confidence weighting, calibration and recommendation
threshold paths by construction.

Current limitation: the contract authenticates an exported manifest's content
and separation rules, but it does not create source snapshots or prove that a
source system retained every historical observation. Exact replay still
depends on timestamped, immutable source data described by the as-of contract.
Manifest bytes must be stored in an append-only artifact store; a SHA-256 is
tamper-evident, not a signature or storage immutability mechanism.
