#!/bin/bash

# backup-restore-test.sh — teste completo de backup/restore com medição de RPO/RTO.
# Uso: ./tools/backup-restore-test.sh

set -euo pipefail

# Configuração
OMNIRA_DATABASE_URL="${OMNIRA_DATABASE_URL:-postgres://omnira:omnira@localhost:55434/omnira_dev?sslmode=disable}"
BACKUP_DIR="${BACKUP_DIR:-.backup-test}"
BACKUP_FILE="${BACKUP_DIR}/omnira-$(date +%s).sql"

# Cores para output
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Parse database URL
parse_db_url() {
    local url="$1"
    # postgres://user:pass@host:port/dbname
    local user=$(echo "$url" | sed -E 's|postgres://([^:]+):.*|\1|')
    local pass=$(echo "$url" | sed -E 's|postgres://[^:]+:([^@]+)@.*|\1|')
    local host=$(echo "$url" | sed -E 's|.*@([^:/]+).*|\1|')
    local port=$(echo "$url" | sed -E 's|.*:([0-9]+)/.*|\1|')
    local dbname=$(echo "$url" | sed -E 's|.*/([^?]+).*|\1|')

    echo "user=$user pass=$pass host=$host port=$port dbname=$dbname"
}

# Extract components
DB_PARAMS=$(parse_db_url "$OMNIRA_DATABASE_URL")
DB_USER=$(echo "$DB_PARAMS" | grep -oP 'user=\K[^ ]*')
DB_HOST=$(echo "$DB_PARAMS" | grep -oP 'host=\K[^ ]*')
DB_PORT=$(echo "$DB_PARAMS" | grep -oP 'port=\K[^ ]*')
DB_NAME=$(echo "$DB_PARAMS" | grep -oP 'dbname=\K[^ ]*')

echo -e "${YELLOW}=== Backup/Restore Validation ===${NC}"
echo "Database: $DB_USER@$DB_HOST:$DB_PORT/$DB_NAME"
echo "Backup directory: $BACKUP_DIR"
echo ""

# Step 1: Backup
echo -e "${YELLOW}[1/5] Creating backup...${NC}"
mkdir -p "$BACKUP_DIR"

BACKUP_START=$(date +%s%N)
PGPASSWORD="omnira" pg_dump \
    --host="$DB_HOST" \
    --port="$DB_PORT" \
    --username="$DB_USER" \
    --format=plain \
    --no-password \
    "$DB_NAME" > "$BACKUP_FILE"
BACKUP_END=$(date +%s%N)

BACKUP_TIME_MS=$(( (BACKUP_END - BACKUP_START) / 1000000 ))
BACKUP_SIZE=$(du -h "$BACKUP_FILE" | cut -f1)

echo -e "${GREEN}✓ Backup created: $BACKUP_FILE ($BACKUP_SIZE, ${BACKUP_TIME_MS}ms)${NC}"
echo ""

# Step 2: Record baseline (count rows)
echo -e "${YELLOW}[2/5] Recording baseline data...${NC}"

BASELINE=$(PGPASSWORD="omnira" psql \
    --host="$DB_HOST" \
    --port="$DB_PORT" \
    --username="$DB_USER" \
    --dbname="$DB_NAME" \
    --no-password \
    --tuples-only \
    --command "
    SELECT
        (SELECT COUNT(*) FROM tenants) as tenants,
        (SELECT COUNT(*) FROM users) as users,
        (SELECT COUNT(*) FROM memberships) as memberships,
        (SELECT COUNT(*) FROM audit_events) as audit_events,
        (SELECT COUNT(*) FROM outbox_events) as outbox_events
    " | tr -d ' ')

echo "Baseline counts: $BASELINE"
echo ""

# Step 3: Simulate data loss (delete some rows)
echo -e "${YELLOW}[3/5] Simulating data loss...${NC}"

PGPASSWORD="omnira" psql \
    --host="$DB_HOST" \
    --port="$DB_PORT" \
    --username="$DB_USER" \
    --dbname="$DB_NAME" \
    --no-password \
    --quiet \
    --command "
    DELETE FROM audit_events;
    DELETE FROM outbox_events;
    "

echo -e "${YELLOW}✓ Deleted audit_events and outbox_events${NC}"
echo ""

# Step 4: Restore from backup
echo -e "${YELLOW}[4/5] Restoring from backup...${NC}"

RESTORE_START=$(date +%s%N)
PGPASSWORD="omnira" psql \
    --host="$DB_HOST" \
    --port="$DB_PORT" \
    --username="$DB_USER" \
    --dbname="$DB_NAME" \
    --no-password \
    --quiet \
    --file="$BACKUP_FILE" > /dev/null 2>&1
RESTORE_END=$(date +%s%N)

RESTORE_TIME_MS=$(( (RESTORE_END - RESTORE_START) / 1000000 ))

echo -e "${GREEN}✓ Restore completed (${RESTORE_TIME_MS}ms)${NC}"
echo ""

# Step 5: Verify data integrity
echo -e "${YELLOW}[5/5] Verifying data integrity...${NC}"

RESTORED=$(PGPASSWORD="omnira" psql \
    --host="$DB_HOST" \
    --port="$DB_PORT" \
    --username="$DB_USER" \
    --dbname="$DB_NAME" \
    --no-password \
    --tuples-only \
    --command "
    SELECT
        (SELECT COUNT(*) FROM tenants) as tenants,
        (SELECT COUNT(*) FROM users) as users,
        (SELECT COUNT(*) FROM memberships) as memberships,
        (SELECT COUNT(*) FROM audit_events) as audit_events,
        (SELECT COUNT(*) FROM outbox_events) as outbox_events
    " | tr -d ' ')

echo "Restored counts: $RESTORED"
echo ""

# Verification
if [ "$BASELINE" = "$RESTORED" ]; then
    echo -e "${GREEN}✓ Data integrity verified: baseline matches restored state${NC}"
else
    echo -e "${RED}✗ Data integrity check FAILED${NC}"
    echo "Baseline: $BASELINE"
    echo "Restored: $RESTORED"
    exit 1
fi

# Summary
echo ""
echo -e "${YELLOW}=== Backup/Restore Report ===${NC}"
echo "RPO (Recovery Point Objective): All data backed up"
echo "RTO (Recovery Time Objective): ${RESTORE_TIME_MS}ms"
echo ""
echo "Backup size: $BACKUP_SIZE"
echo "Backup time: ${BACKUP_TIME_MS}ms"
echo "Restore time: ${RESTORE_TIME_MS}ms"
echo ""
echo -e "${GREEN}✓ Backup/restore validation PASSED${NC}"

# Cleanup
rm -f "$BACKUP_FILE"
rmdir "$BACKUP_DIR" 2>/dev/null || true

exit 0
