#!/bin/bash
set -e

echo ">>> Waiting for primary to be ready..."
until pg_isready -h postgres-master -p 5432 -U postgres; do
  echo "    Primary not ready — retrying in 2s..."
  sleep 2
done

echo ">>> Primary is ready. Clearing data directory for base backup..."
rm -rf /var/lib/postgresql/data/*

echo ">>> Taking base backup from primary..."
PGPASSWORD=replicator_password pg_basebackup \
  -h postgres-master \
  -U replicator \
  -D /var/lib/postgresql/data \
  -P \
  -Xs \
  -R

echo ">>> Base backup complete. Overlaying replica config..."
cp /etc/replica/postgresql.conf /var/lib/postgresql/data/postgresql.conf

echo ">>> Starting replica in hot standby mode..."
exec docker-entrypoint.sh postgres