#!/bin/sh
# The docker-compose.yaml showcase's Actions runners: each replica
# (`docker compose up -d --scale runner=3`) registers itself on start with
# the reusable registration token the seed job leaves in the ogit-data
# volume, then takes runs. Its state stays in the container, so replicas
# never share a runner; runners left behind by restarts are removed after a
# day offline. Job containers join the Compose network and reach the server
# there, at http://ogit:8080 (--job-url), not at its public URL (localhost
# would be the job container itself).
set -eu
mkdir -p /runner
cd /runner

echo "runner: waiting for a registration token from the seed job"
until [ -s /seed/runner-registration-token ]; do sleep 2; done
/app runner register --url http://ogit:8080 --token "$(cat /seed/runner-registration-token)" --name "showcase-$(hostname)"

exec /app runner run --network "${RUNNER_NETWORK:-ogit_default}" --job-url "${RUNNER_JOB_URL:-http://ogit:8080}"
