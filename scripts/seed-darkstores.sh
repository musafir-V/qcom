#!/bin/bash

# Seed sample darkstores (with serviceable-area polygons) into DynamoDB.
# Usage: ./scripts/seed-darkstores.sh

set -e

REGION="${DYNAMODB_REGION:-us-east-1}"
TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
ENDPOINT="${DYNAMODB_ENDPOINT:-http://localhost:8000}"
NOW="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

echo "Seeding darkstores to DynamoDB..."
echo "Table: $TABLE_NAME  Endpoint: $ENDPOINT"
echo ""

# DS-001 — Indiranagar, Bengaluru. Polygon: lat 12.96-12.99, lng 77.62-77.66.
aws dynamodb put-item \
  --table-name "$TABLE_NAME" \
  --region "$REGION" \
  --endpoint-url "$ENDPOINT" \
  --item '{
    "PK": {"S": "DARKSTORE!DS-001"},
    "SK": {"S": "METADATA"},
    "darkstore_id": {"S": "DS-001"},
    "name": {"S": "Indiranagar Darkstore"},
    "latitude": {"N": "12.9719"},
    "longitude": {"N": "77.6412"},
    "is_active": {"BOOL": true},
    "created_at": {"S": "'"$NOW"'"},
    "updated_at": {"S": "'"$NOW"'"},
    "polygon": {"L": [
      {"M": {"lat": {"N": "12.96"}, "lng": {"N": "77.62"}}},
      {"M": {"lat": {"N": "12.96"}, "lng": {"N": "77.66"}}},
      {"M": {"lat": {"N": "12.99"}, "lng": {"N": "77.66"}}},
      {"M": {"lat": {"N": "12.99"}, "lng": {"N": "77.62"}}}
    ]}
  }' \
  || echo "Note: DS-001 may already exist"

# DS-002 — Koramangala, Bengaluru. Polygon: lat 12.91-12.95, lng 77.60-77.64.
aws dynamodb put-item \
  --table-name "$TABLE_NAME" \
  --region "$REGION" \
  --endpoint-url "$ENDPOINT" \
  --item '{
    "PK": {"S": "DARKSTORE!DS-002"},
    "SK": {"S": "METADATA"},
    "darkstore_id": {"S": "DS-002"},
    "name": {"S": "Koramangala Darkstore"},
    "latitude": {"N": "12.9352"},
    "longitude": {"N": "77.6245"},
    "is_active": {"BOOL": true},
    "created_at": {"S": "'"$NOW"'"},
    "updated_at": {"S": "'"$NOW"'"},
    "polygon": {"L": [
      {"M": {"lat": {"N": "12.91"}, "lng": {"N": "77.60"}}},
      {"M": {"lat": {"N": "12.91"}, "lng": {"N": "77.64"}}},
      {"M": {"lat": {"N": "12.95"}, "lng": {"N": "77.64"}}},
      {"M": {"lat": {"N": "12.95"}, "lng": {"N": "77.60"}}}
    ]}
  }' \
  || echo "Note: DS-002 may already exist"

# Index item (PK=DARKSTORE, SK=INDEX) — holds the set of all darkstore IDs so the
# app can fetch darkstores by primary key instead of scanning the whole table.
# ADD is idempotent on a String Set, so re-running the seed is safe.
aws dynamodb update-item \
  --table-name "$TABLE_NAME" \
  --region "$REGION" \
  --endpoint-url "$ENDPOINT" \
  --key '{"PK": {"S": "DARKSTORE"}, "SK": {"S": "INDEX"}}' \
  --update-expression "ADD darkstore_ids :ids SET updated_at = :now" \
  --expression-attribute-values '{
    ":ids": {"SS": ["DS-001", "DS-002"]},
    ":now": {"S": "'"$NOW"'"}
  }' \
  || echo "Note: failed to update darkstore index"

echo ""
echo "Darkstores seeded successfully!"
echo ""
echo "Test serviceable (inside DS-001):   latitude 12.9719, longitude 77.6412"
echo "Test unserviceable (no polygon):    latitude 13.5000, longitude 77.6000"
