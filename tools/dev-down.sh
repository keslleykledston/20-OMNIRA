#!/bin/bash
# dev-down.sh — derruba infrastructure local

set -e

echo "Stopping OMNIRA services..."
docker compose down --remove-orphans

echo "Done."
