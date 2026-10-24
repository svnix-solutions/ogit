#!/bin/sh

set -e

./app migration -c /config/config.yaml
exec ./app server -c /config/config.yaml