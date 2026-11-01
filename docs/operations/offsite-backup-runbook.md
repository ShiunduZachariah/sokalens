# Off-site backup runbook

Implements the code half of in-play architecture §17. The other half — the
bucket, the credentials, and the passphrase custody — is owner action, and §17
is right that it is the half that actually decides whether a restore works.

---

## 1. What changed, and what did not

`infra/deploy/backup.sh` already produced a **restore-verified** local recovery
point: dump, checksum, `pg_restore --list`, a real restore into a scratch
database, a per-table row-count comparison with bounded drift, and a 14-day
local retention sweep. None of that was touched.

What changed is the copy that leaves the host:

| | Before | Now |
|---|---|---|
| Encryption | none | AES-256 on this host, before upload |
| Upload without a key | silent | refused |
| Destination | `R2_*` only | any S3-compatible endpoint; `R2_*` still works |
| Ciphertext verified | — | decrypted back before upload |
| Remote deletion | none | still none, deliberately |
| Local ciphertext | — | removed on every exit path |

## 2. Why encryption is not optional here

The dump contains account emails and password hashes. Provider-side encryption
does not help: it protects the disk the provider owns, not the object from
whoever can read the bucket. A single misconfigured ACL is the whole user table.

So the script refuses to upload plaintext. `BACKUP_ALLOW_PLAINTEXT_UPLOAD=true`
exists as an escape hatch, prints a warning, and should be treated as a
temporary state with a ticket attached.

The ciphertext is decrypted back to `/dev/null` before it is uploaded. An
off-site backup that will not open is indistinguishable from no off-site backup,
and the difference is only discovered on the day it matters.

## 3. The passphrase is now a single point of failure. Treat it as one.

**If the passphrase exists only on the VPS, an encrypted off-site backup is
worthless the moment the VPS dies** — which is the exact scenario it was created
for. This is the most important sentence in this document.

- Generate it somewhere other than the server.
- Store it in a password manager, and in one offline place.
- Do not rotate it without re-encrypting or retaining the old key: every
  existing object stays readable only by the key that made it.
- It is not a secret the deploy pipeline needs. It belongs in the server's
  environment file and nowhere else in the repository or CI.

## 4. Choose B2 over R2, and not because of price

Both start with 10 GB free, and at this scale the cost difference is a couple of
dollars a year. The deciding factor is **correlated failure**.

Cloudflare already holds DNS, TLS, CDN and Turnstile for this property. Putting
backups there too means one account problem — a billing failure, a compromise, a
suspension — takes out production *and* the recovery path at the same moment.
An independent login with independent billing is the entire point of an off-site
copy.

R2 remains the better choice later for user-facing assets, where zero egress and
edge integration are the actual benefit. That is a different bucket for a
different purpose.

Egress is a non-issue either way: R2 never charges it, and B2 gives free egress
up to three times average monthly storage, while a full restore is one times.

## 5. Owner actions

### 5.1 Create the bucket

1. Create a Backblaze B2 account with **its own login and billing**, not
   federated through anything Cloudflare holds.
2. Create a private bucket, e.g. `sokalens-backups`.
3. Create an application key scoped to that bucket only. It needs write and
   list; it does **not** need delete.
4. Note the S3-compatible endpoint (`https://s3.<region>.backblazeb2.com`).

### 5.2 Turn on Object Lock

Object Lock in governance or compliance mode, with a retention that matches
§17.2: 7 daily, 4 weekly, 6 monthly.

This is the defence against a compromised host deleting its own backups — the
failure that turns a bad day into a fatal one. It is also why **this script
never deletes a remote object**, and why the application key should not have
delete permission: off-site retention belongs to a bucket lifecycle rule the
server has no credentials to change.

### 5.3 Configure the server

```bash
S3_ENDPOINT=https://s3.eu-central-003.backblazeb2.com
S3_ACCESS_KEY_ID=...
S3_SECRET_ACCESS_KEY=...
R2_BUCKET=sokalens-backups
BACKUP_ENCRYPTION_PASSPHRASE=...
```

`aws` CLI and `gpg` must be present on the host. `R2_BUCKET` keeps its name
because it is the bucket variable the script already used; renaming it would
break a running deployment for no benefit.

### 5.4 Verify, from the other end

Running the backup proves the upload works. It does **not** prove you can
restore. Do this once now and quarterly thereafter:

```bash
# On a machine that is NOT the VPS.
aws s3 cp s3://sokalens-backups/postgres/<timestamp>.dump.gpg . --endpoint-url "$S3_ENDPOINT"
gpg --decrypt --output restored.dump <timestamp>.dump.gpg
pg_restore --list restored.dump | head
```

Doing it from another machine is the point: it tests the passphrase custody and
the credentials, not just the file. An untested backup is a guess.

## 6. What is still not done

- **restic or rclone.** §17.2 prefers them over raw `pg_dump` uploads, mainly
  for deduplication — day-to-day Postgres deltas are small, so retained
  snapshots would cost far less than naive full copies. This ships plain
  encrypted dumps, which is correct but not space-efficient. Worth revisiting
  when off-site storage cost becomes visible; the S3 seam does not change.
- **Automated restore drills.** The quarterly restore above is a human
  procedure. Automating it needs a scratch host, which does not exist.
- **Alerting on a missed backup.** Nothing currently notices if the cron stops
  running. The `/healthz` document is the natural place to surface the age of
  the last successful backup, in the same shape as the live-poll age.
