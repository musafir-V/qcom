#!/bin/bash

# Script to seed the PAGE#HOME data in DynamoDB for testing
# Usage: ./scripts/seed-home-page.sh

set -e

# Configuration
REGION="${DYNAMODB_REGION:-us-east-1}"
TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"
ENDPOINT="${DYNAMODB_ENDPOINT:-http://localhost:8000}"

echo "Seeding home page data to DynamoDB..."
echo "Region: $REGION"
echo "Table: $TABLE_NAME"
echo "Endpoint: $ENDPOINT"
echo ""

# Create sample home page data
aws dynamodb put-item \
  --table-name "$TABLE_NAME" \
  --region "$REGION" \
  --endpoint-url "$ENDPOINT" \
  --item '{
    "PK": {"S": "PAGE#HOME"},
    "SK": {"S": "PAGE#HOME"},
    "content": {
      "M": {
        "title": {"S": "Welcome to QCom"},
        "subtitle": {"S": "Your trusted platform"},
        "sections": {
          "L": [
            {
              "M": {
                "id": {"S": "section-1"},
                "type": {"S": "hero"},
                "title": {"S": "Featured Products"},
                "description": {"S": "Check out our latest offerings"}
              }
            },
            {
              "M": {
                "id": {"S": "section-2"},
                "type": {"S": "grid"},
                "title": {"S": "Categories"},
                "items": {
                  "L": [
                    {
                      "M": {
                        "id": {"S": "cat-1"},
                        "name": {"S": "Electronics"},
                        "icon": {"S": "electronics-icon"}
                      }
                    },
                    {
                      "M": {
                        "id": {"S": "cat-2"},
                        "name": {"S": "Fashion"},
                        "icon": {"S": "fashion-icon"}
                      }
                    }
                  ]
                }
              }
            }
          ]
        },
        "version": {"N": "1"}
      }
    }
  }' \
  || echo "Note: Item may already exist"

echo ""
echo "✅ Home page data seeded successfully!"
echo ""
echo "To verify, query the data:"
echo "aws dynamodb get-item --table-name $TABLE_NAME --region $REGION --endpoint-url $ENDPOINT --key '{\"PK\":{\"S\":\"PAGE#HOME\"},\"SK\":{\"S\":\"PAGE#HOME\"}}'"

