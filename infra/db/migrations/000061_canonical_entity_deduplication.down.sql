-- The merge itself is not reversible: once duplicate rows are folded into a
-- canonical row and the redundant ones deleted, the split cannot be
-- reconstructed from what remains. Recovery is a restore from the pre-deploy
-- backup, not a down migration.
--
-- This drops only the structures the up migration added, so the schema returns
-- to its previous shape. The alias rows are lost with the tables, which is
-- correct: they describe a merge that a restore would undo anyway.

BEGIN;

DROP TABLE IF EXISTS team_aliases;
DROP TABLE IF EXISTS league_aliases;
DROP TABLE IF EXISTS entity_merge_conflicts;
DROP FUNCTION IF EXISTS canonical_entity_key(TEXT);

COMMIT;
