# Backups

A complete backup has four parts. Restoring needs all of them.

| Part | What | How |
|---|---|---|
| Database | users, permissions, pull requests, issues, settings, encrypted secrets | SQLite: `sqlite3 database.sqlite ".backup backup.sqlite"`; PostgreSQL: `pg_dump` |
| Storage | git data, wikis, LFS objects, release assets, packages, Actions files | local: copy the directory; S3: bucket versioning, replication or a copy |
| Configuration | `config.yaml` and the environment variables you set | your configuration management |
| Keys | `session.secret_key`, `security.encryption_key`, the SSH host key | a secret store |

::: danger Keep security.encryption_key
Without the same `security.encryption_key`, a restored database can't decrypt its secrets: authenticator app seeds, SSO client secrets, mirror credentials and Actions secrets.
:::

## Order and consistency

The database refers to the storage (repositories, assets, packages), not the other way around. To keep them consistent:

- **Simplest:** stop the server, back up both, start it again.
- **While running:** back up the database first, then the storage. Data written in between is extra in the storage, which is harmless. The other way round, the database would name files the backup doesn't have.

On S3, packs never change once written; only refs, HEAD and a few small files do. A bucket with versioning keeps every version, so you can restore to a point in time, but delete markers and old versions cost storage: add a lifecycle rule. Deleted repositories' data is removed from the bucket, so versioning is also how you recover them.

The [backup and restore guide](/guides/backup-restore) has scripts for both storage types.
