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

echo ""
echo "Darkstores seeded successfully!"
echo ""
echo "Test serviceable (inside DS-001):   latitude 12.9719, longitude 77.6412"
echo "Test unserviceable (no polygon):    latitude 13.5000, longitude 77.6000"
