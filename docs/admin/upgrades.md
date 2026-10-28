# Upgrades

1. Read the release notes for the version you're moving to.
2. [Back up](./backups) the database, the storage and the configuration.
3. Stop the server (or all of them).
4. Replace the binary or the image tag.
5. Run the migrations once: `ogit migration -c config.yaml`. The Docker image runs them on every start.
6. Start the server (or the servers).

Migrations change the database for the new version. The old version doesn't know the new schema, so going back means restoring the backup from step 2.

## Several servers

Run the migration from one place before starting any new server: a one-off job, or one server started first. With the Docker image, start one container, wait for it to be healthy, then start the others: migrations that were already applied are skipped.

Old and new servers shouldn't run side by side against the same database, since the old one doesn't know the new schema.

## Runners

`ogit runner` speaks a protocol to the server; upgrade runners together with the server.
