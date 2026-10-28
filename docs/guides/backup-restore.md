# Back up and restore

This guide backs up an oGit server and restores it on a new host. See [Backups](/admin/backups) for what each part is.

## Local storage and SQLite

A nightly script, without stopping the server. The database goes first, then the storage:

```bash
#!/bin/sh
set -e
DEST=/backups/ogit/$(date +%F)
mkdir -p "$DEST"

sqlite3 /data/database.sqlite ".backup '$DEST/database.sqlite'"   # a consistent copy while running
tar -C /data -czf "$DEST/repositories.tar.gz" repositories
cp /data/ssh_host_ed25519_key "$DEST/"
cp /etc/ogit/config.yaml "$DEST/"

find /backups/ogit -mindepth 1 -maxdepth 1 -mtime +14 -exec rm -rf {} +
```

Don't copy the SQLite file with `cp` while the server runs: use `.backup`, which takes a consistent snapshot.

## PostgreSQL and S3

```bash
#!/bin/sh
set -e
DEST=/backups/ogit/$(date +%F)
mkdir -p "$DEST"

pg_dump --format=custom --file="$DEST/ogit.dump" "$DATABASE_URL"
aws s3 sync s3://ogit "s3://ogit-backup/$(date +%F)/" --endpoint-url "$S3_ENDPOINT"
```

Instead of copying the bucket, you can turn on versioning and replication: packs never change, so only refs and small files get new versions. Add a lifecycle rule that expires old versions after your retention period.

Keep the configuration and the keys (`SESSION_SECRET_KEY`, `SECURITY_ENCRYPTION_KEY`, the SSH host key) in your secret store, not next to the backups.

## Restore

1. Install the **same oGit version** as the backup (see [Installation](/admin/install)).
2. Put back the configuration and the keys. Use the same `security.encryption_key`.
3. Restore the database:
   - SQLite: copy `database.sqlite` to `database.dsn`'s path;
   - PostgreSQL: create an empty database, then `pg_restore --no-owner --dbname=ogit ogit.dump`.
4. Restore the storage: unpack `repositories.tar.gz` to `storage.local.path`, or copy the objects back into the bucket.
5. Put back the SSH host key at `ssh.host_key_path`.
6. Run `ogit migration` (a no-op for the same version) and start the server.

## Check it

- Sign in, and open a few repositories, their pull requests and wikis.
- Clone a repository over HTTP and SSH, and push to a test branch.
- Download an LFS file and a release asset.
- Sign in with an authenticator app code: if it fails, the encryption key isn't the same.

Practice a restore on a spare host now and then: a backup only counts once it has been restored.
