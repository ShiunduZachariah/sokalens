#!/usr/bin/env bash
set -euo pipefail

# Only the dedicated rootless CI daemon owns these throwaway Postgres volumes.
expected_root=/home/ci/.local/share/docker
actual_root=$(docker info --format '{{.DockerRootDir}}')
if [[ "$actual_root" != "$expected_root" ]]; then
  echo "Refusing CI volume cleanup: Docker root is $actual_root" >&2
  exit 1
fi

# Other CI runs can share this daemon. Leave all volumes alone while a job is
# using containers, then let the next nightly run retry.
if [[ -n "$(docker ps --quiet)" ]]; then
  echo 'Skipping CI volume cleanup: containers are still running.'
  exit 0
fi

before=$(docker volume ls --quiet | wc -l)
echo "CI volumes before cleanup: $before"
docker volume prune --force
after=$(docker volume ls --quiet | wc -l)
echo "CI volumes after cleanup: $after"
