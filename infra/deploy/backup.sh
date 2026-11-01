#!/usr/bin/env bash
# Create a restore-verified local PostgreSQL recovery point. R2 upload is
# optional: local verification must work before off-site backup is configured.
set -euo pipefail

TIMESTAMP="$(date -u +%Y%m%dT%H%M%SZ)"
DUMP_DIR="${BACKUP_DIR:-/home/deploy/backups/sokalens}"
CONTAINER="${POSTGRES_CONTAINER:-sports-prediction-platform-postgres-1}"
DB_USER="${POSTGRES_USER:-sports}"
DB_NAME="${POSTGRES_DB:-sports_prediction}"
BUCKET="${R2_BUCKET:-sokalens-backups}"
DUMP_FILE="$DUMP_DIR/sokalens-$TIMESTAMP.dump"
CHECKSUM_FILE="$DUMP_FILE.sha256"
RESTORE_DB="sokalens_restore_${TIMESTAMP,,}_$$"
RESTORE_DB="${RESTORE_DB//[^a-z0-9_]/_}"
RESTORE_CREATED=0

# The encrypted copy is a temporary artefact of the upload. Removing it from
# the EXIT trap rather than after a successful upload matters: `set -e` means
# a failed upload exits before any cleanup line is reached, which would leave
# encrypted account emails and password hashes on the host indefinitely.
ENCRYPTED_FILE=""

cleanup_run() {
  if [ "$RESTORE_CREATED" -eq 1 ]; then
    docker exec "$CONTAINER" dropdb --if-exists -U "$DB_USER" "$RESTORE_DB" >/dev/null
  fi
  if [ -n "$ENCRYPTED_FILE" ]; then
    rm -f "$ENCRYPTED_FILE"
  fi
}
trap cleanup_run EXIT

mkdir -p "$DUMP_DIR"
chmod 700 "$DUMP_DIR"
umask 077

docker exec "$CONTAINER" \
  pg_dump -U "$DB_USER" -d "$DB_NAME" --format=custom --compress=6 > "$DUMP_FILE"
sha256sum "$DUMP_FILE" > "$CHECKSUM_FILE"
sha256sum --check "$CHECKSUM_FILE"
docker exec -i "$CONTAINER" pg_restore --list < "$DUMP_FILE" >/dev/null

docker exec "$CONTAINER" createdb -U "$DB_USER" "$RESTORE_DB"
RESTORE_CREATED=1
docker exec -i "$CONTAINER" pg_restore \
  -U "$DB_USER" -d "$RESTORE_DB" --no-owner --no-privileges --exit-on-error < "$DUMP_FILE"

critical_counts() {
  local database="$1"
  docker exec "$CONTAINER" psql -U "$DB_USER" -d "$database" -At -F '|' -c "
    SELECT 'schema_migrations', count(*) FROM schema_migrations
    UNION ALL SELECT 'engine_predictions', count(*) FROM engine_predictions
    UNION ALL SELECT 'fixtures', count(*) FROM fixtures
    UNION ALL SELECT 'model_evaluation_facts', count(*) FROM model_evaluation_facts
    UNION ALL SELECT 'prediction_accuracy_daily', count(*) FROM prediction_accuracy_daily
    ORDER BY 1;"
}

SOURCE_COUNTS="$(critical_counts "$DB_NAME")"
RESTORE_COUNTS="$(critical_counts "$RESTORE_DB")"
SOURCE_MIGRATIONS="$(docker exec "$CONTAINER" psql -U "$DB_USER" -d "$DB_NAME" -At -c 'SELECT version FROM schema_migrations ORDER BY version')"
RESTORE_MIGRATIONS="$(docker exec "$CONTAINER" psql -U "$DB_USER" -d "$RESTORE_DB" -At -c 'SELECT version FROM schema_migrations ORDER BY version')"
[ "$SOURCE_MIGRATIONS" = "$RESTORE_MIGRATIONS" ] || { echo "ERROR: restored schema migration list differs" >&2; exit 1; }

# The live worker may add rows while the restore is checked. Compare every key
# table and permit only a small bounded drift instead of requiring a racy exact
# equality. Empty/non-empty mismatches and large differences still fail closed.
while IFS='|' read -r table source_count; do
  restore_count="$(printf '%s\n' "$RESTORE_COUNTS" | awk -F '|' -v name="$table" '$1 == name { print $2 }')"
  [ -n "$restore_count" ] || { echo "ERROR: restored count missing for $table" >&2; exit 1; }
  if [ "$source_count" -eq 0 ] || [ "$restore_count" -eq 0 ]; then
    [ "$source_count" -eq "$restore_count" ] || { echo "ERROR: empty-table mismatch for $table" >&2; exit 1; }
    continue
  fi
  delta=$((source_count - restore_count))
  [ "$delta" -ge 0 ] || delta=$((-delta))
  allowed=$((source_count / 20))
  [ "$allowed" -ge 10000 ] || allowed=10000
  [ "$delta" -le "$allowed" ] || { echo "ERROR: restored count drift too large for $table" >&2; exit 1; }
done <<< "$SOURCE_COUNTS"

docker exec "$CONTAINER" dropdb -U "$DB_USER" "$RESTORE_DB"
RESTORE_CREATED=0

# --- Off-site copy (in-play architecture 17) -------------------------------
#
# The dump contains account emails and password hashes. A misconfigured bucket
# must not expose user PII, so the off-site copy is encrypted on this host
# before it leaves it -- never by the storage provider on our behalf, because
# provider-side encryption protects the disk they own, not the object from
# whoever can read the bucket.
#
# Destination is any S3-compatible endpoint. 17.1 argues for Backblaze B2 over
# Cloudflare R2, and the reason is correlated failure rather than price:
# Cloudflare already holds DNS, TLS, CDN and Turnstile, so putting backups there
# too means one account problem -- billing, compromise, suspension -- takes out
# production *and* the recovery path at the same moment. The S3_* variables are
# the seam; switching provider is a config line. R2_* still works, so an
# existing deployment keeps running untouched.
S3_ENDPOINT="${S3_ENDPOINT:-${R2_ENDPOINT:-}}"
S3_ACCESS_KEY_ID="${S3_ACCESS_KEY_ID:-${R2_ACCESS_KEY_ID:-}}"
S3_SECRET_ACCESS_KEY="${S3_SECRET_ACCESS_KEY:-${R2_SECRET_ACCESS_KEY:-}}"
UPLOAD_FILE="$DUMP_FILE"
UPLOAD_SUFFIX="dump"

if [ -n "$S3_ENDPOINT" ] && [ -n "$S3_ACCESS_KEY_ID" ] && [ -n "$S3_SECRET_ACCESS_KEY" ]; then
  command -v aws >/dev/null 2>&1 || { echo "ERROR: aws CLI is required for the configured off-site upload" >&2; exit 1; }

  if [ -n "${BACKUP_ENCRYPTION_PASSPHRASE:-}" ]; then
    command -v gpg >/dev/null 2>&1 || { echo "ERROR: gpg is required to encrypt the off-site copy" >&2; exit 1; }
    ENCRYPTED_FILE="$DUMP_FILE.gpg"
    printf %s "$BACKUP_ENCRYPTION_PASSPHRASE" | gpg --batch --quiet --yes \
      --symmetric --cipher-algo AES256 --s2k-mode 3 --s2k-count 65011712 \
      --passphrase-fd 0 --output "$ENCRYPTED_FILE" "$DUMP_FILE"

    # Prove the ciphertext opens before it becomes the only copy that survives
    # a host loss. An unopenable off-site backup is indistinguishable from no
    # off-site backup, and the difference is only discovered on the worst day.
    printf %s "$BACKUP_ENCRYPTION_PASSPHRASE" | gpg --batch --quiet --yes \
      --decrypt --passphrase-fd 0 --output /dev/null "$ENCRYPTED_FILE" \
      || { echo "ERROR: encrypted backup failed to decrypt; refusing to upload" >&2; exit 1; }

    UPLOAD_FILE="$ENCRYPTED_FILE"
    UPLOAD_SUFFIX="dump.gpg"
  elif [ "${BACKUP_ALLOW_PLAINTEXT_UPLOAD:-false}" = "true" ]; then
    # Deliberately awkward to reach. Anyone who can read the bucket then has
    # every account email and password hash.
    echo "WARNING: uploading an UNENCRYPTED dump containing account emails and password hashes" >&2
  else
    echo "ERROR: refusing to upload an unencrypted dump containing user PII." >&2
    echo "       Set BACKUP_ENCRYPTION_PASSPHRASE, or BACKUP_ALLOW_PLAINTEXT_UPLOAD=true to override." >&2
    exit 1
  fi

  AWS_ACCESS_KEY_ID="$S3_ACCESS_KEY_ID" AWS_SECRET_ACCESS_KEY="$S3_SECRET_ACCESS_KEY" \
    aws s3 cp "$UPLOAD_FILE" "s3://$BUCKET/postgres/$TIMESTAMP.$UPLOAD_SUFFIX" \
      --endpoint-url "$S3_ENDPOINT" --no-progress
  echo "offsite: s3://$BUCKET/postgres/$TIMESTAMP.$UPLOAD_SUFFIX"

  # Nothing here deletes a remote object, and that is deliberate. 17.2 wants
  # Object Lock precisely so a compromised host cannot erase its own backups --
  # the failure that turns a bad day into a fatal one. Off-site retention
  # belongs to a bucket lifecycle rule this host has no credentials to change,
  # not to a script running on the host being protected.
  #
  # The local ciphertext is removed by the EXIT trap, so it goes on every path
  # rather than only on the one that reaches the end.
fi

# Keep the newest verified recovery point locally, and nothing else.
#
# This kept fourteen days, which sounds prudent and was not. Fixtures, odds and
# predictions stream in continuously, so a dump from last Tuesday describes a
# world that no longer exists -- it cannot be restored into production without
# throwing away everything since, which means it would never actually be used.
# What it did do was occupy 26GB of a 99GB disk: one dump per deploy, several
# deploys a day, each one 741MB and growing (161MB a fortnight earlier).
#
# The local copy exists for one job: restoring the mistake you just made,
# within minutes, while you still remember what you did. One is enough for
# that. History belongs off-site in R2, where it is versioned and costs no
# local disk.
#
# Ordered by modification time and pruned only after this dump has been
# verified and uploaded, so a failed run leaves the previous recovery point
# standing -- the disk is never without one. Deleting only this explicit
# filename pattern cannot touch PostgreSQL volumes or unrelated backups.
KEEP_LOCAL_DUMPS="${KEEP_LOCAL_DUMPS:-1}"
mapfile -t OBSOLETE_DUMPS < <(
  find "$DUMP_DIR" -maxdepth 1 -type f -name 'sokalens-*.dump' -printf '%T@ %p\n' |
    sort -rn |
    tail -n "+$((KEEP_LOCAL_DUMPS + 1))" |
    cut -d' ' -f2-
)
for obsolete in "${OBSOLETE_DUMPS[@]}"; do
  [ -n "$obsolete" ] || continue
  # The .gpg is a backstop for a run killed hard enough to skip its own EXIT
  # trap. In normal operation the trap has already removed it; leaving one
  # behind would strand an encrypted copy of the database on the disk.
  rm -f -- "$obsolete" "$obsolete.sha256" "$obsolete.gpg"
done

echo "backup verified: $DUMP_FILE"
echo "checksum: $CHECKSUM_FILE"
