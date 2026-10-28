#!/bin/sh
# The docker-compose.yaml showcase's Actions runner: registers once, with
# the registration token the seed job leaves in the ogit-data volume, then
# takes runs. Job containers join the Compose network to reach the server.
set -eu
cd /runner

if [ ! -s .runner ]; then
  echo "runner: waiting for a registration token from the seed job"
  until [ -s /seed/runner-token ]; do sleep 2; done
  /app runner register --url http://ogit:8080 --token "$(cat /seed/runner-token)" --name showcase
fi

exec /app runner run --network "${RUNNER_NETWORK:-ogit_default}"
