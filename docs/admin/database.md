# Database

oGit supports SQLite and PostgreSQL.

| | SQLite | PostgreSQL |
|---|---|---|
| Setup | nothing: a file | a server |
| Servers | one | several, sharing it |
| Foreign keys | not enforced (oGit deletes dependent rows itself) | enforced |
| Backups | copy the file safely (see [Backups](./backups)) | `pg_dump` |

```yaml
database:
  dialect: sqlite
  dsn: ./database.sqlite
```

```yaml
database:
  dialect: postgres
  dsn: "host=db user=ogit password=… dbname=ogit port=5432 sslmode=require"
```

The PostgreSQL DSN is a libpq connection string. A `postgres://` URL works too.

## Migrations

`ogit migration -c config.yaml` creates the schema and applies new migrations. It is safe to run on every start: applied migrations are skipped. With several servers, run it once, from one of them or from a separate job, before starting the new version. See [Upgrades](./upgrades).

Migrations only move forward in normal use. Restore a backup to go back to an older version.

## Moving from SQLite to PostgreSQL

There is no built-in converter yet. Choose PostgreSQL from the start if you expect to run more than one server.
