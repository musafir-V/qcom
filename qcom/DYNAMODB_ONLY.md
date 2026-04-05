# DynamoDB-Only Implementation

## Overview

The QCom authentication service now uses **DynamoDB exclusively** for all data storage, eliminating the need for Redis/Valkey. This simplifies the architecture and reduces operational complexity, especially for AWS deployments.

## What Changed

### ✅ Removed
- Redis/Valkey dependencies
- go-redis client library
- Redis configuration and connection management
- Complex cache integration issues

### ✅ Added
- `OTPRepository` - DynamoDB storage for OTPs with TTL
- `RefreshTokenRepository` - DynamoDB storage for refresh tokens with TTL
- Automatic expiration using DynamoDB TTL feature

## Architecture

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │
       ▼
┌──────────────┐
│  Go Server   │
└──────┬───────┘
       │
       ▼
┌─────────────────────────────┐
│        DynamoDB             │
├─────────────────────────────┤
│ Users      (PK: USER#phone) │
│ OTPs       (PK: OTP#phone)  │ ← TTL enabled
│ Tokens     (PK: REFRESH_TOKEN#jti) │ ← TTL enabled
│ Revoked    (PK: REVOKED_TOKEN#jti) │ ← TTL enabled
└─────────────────────────────┘
```

## DynamoDB Table Schema

### Single Table Design

**Table Name:** `QComTable`

**Primary Key:**
- **PK** (Partition Key): String
- **SK** (Sort Key): String

**TTL Attribute:** `TTL` (Number, Unix timestamp in seconds)

### Item Types

#### 1. User Records
```
PK: USER#+1234567890
SK: METADATA
Attributes:
  - phone_number
  - name
  - created_at
  - updated_at
```

#### 2. OTP Records (with TTL)
```
PK: OTP#+1234567890
SK: METADATA
Attributes:
  - OTPHash (bcrypt hash)
  - Phone
  - Attempts
  - CreatedAt
  - ExpiresAt
  - TTL (Unix timestamp for auto-deletion)
```

#### 3. Test OTP Records (with TTL)
```
PK: OTP_TEST#+1234567890
SK: METADATA
Attributes:
  - OTP (plain text, for testing only)
  - ExpiresAt
  - TTL
```

#### 4. Refresh Token Records (with TTL)
```
PK: REFRESH_TOKEN#<jti>
SK: METADATA
Attributes:
  - JTI
  - UserID
  - Phone
  - FamilyID
  - Revoked
  - CreatedAt
  - ExpiresAt
  - TTL
```

#### 5. Revoked Token Markers (with TTL)
```
PK: REVOKED_TOKEN#<jti>
SK: METADATA
Attributes:
  - RevokedAt
  - TTL
```

## TTL (Time To Live)

### How It Works

1. **Automatic Cleanup**: DynamoDB automatically deletes expired items within 48 hours
2. **No Manual Deletion**: No need to manually clean up expired OTPs or tokens
3. **Cost Effective**: TTL deletions are free (no write capacity consumed)

### TTL Configuration

The `create-table.sh` script now automatically enables TTL:

```bash
aws dynamodb update-time-to-live \
  --table-name QComTable \
  --time-to-live-specification "Enabled=true,AttributeName=TTL"
```

### TTL Calculation

```go
// For OTPs (10 minutes by default)
ttl := time.Now().Add(10 * time.Minute).Unix()

// For refresh tokens (7 days by default)
ttl := time.Now().Add(7 * 24 * time.Hour).Unix()
```

## Environment Variables

### Simplified Configuration

```bash
# DynamoDB (required)
export DYNAMODB_ENDPOINT="http://localhost:8000"  # Local dev
export DYNAMODB_REGION="us-east-1"
export DYNAMODB_TABLE_NAME="QComTable"

# JWT (required)
export JWT_SECRET_KEY="your-32-char-secret-key-here-12345"
export JWT_ACCESS_EXPIRY="15m"
export JWT_REFRESH_EXPIRY="168h"

# OTP (optional)
export OTP_LENGTH="6"
export OTP_EXPIRY="10m"
export OTP_MAX_ATTEMPTS="5"

# Server (optional)
export PORT="8080"
```

**Note:** No Redis environment variables needed!

## Setup Instructions

### 1. Start DynamoDB (Local Development)

```bash
# Using Docker Compose
docker-compose up -d

# Or using Make
make docker-up
```

### 2. Create Table with TTL

```bash
# Run the setup script
./scripts/create-table.sh

# Or using Make
make setup
```

### 3. Run the Application

```bash
# Set environment variables
export JWT_SECRET_KEY="your-secret-key-32-characters"
export DYNAMODB_ENDPOINT="http://localhost:8000"
export DYNAMODB_TABLE_NAME="QComTable"

# Run the server
./bin/qcom-server

# Or using Make
make run
```

## AWS Deployment

### 1. Create DynamoDB Table

```bash
aws dynamodb create-table \
  --table-name QComTable \
  --attribute-definitions \
    AttributeName=PK,AttributeType=S \
    AttributeName=SK,AttributeType=S \
  --key-schema \
    AttributeName=PK,KeyType=HASH \
    AttributeName=SK,KeyType=RANGE \
  --billing-mode PAY_PER_REQUEST \
  --region us-east-1
```

### 2. Enable TTL

```bash
aws dynamodb update-time-to-live \
  --table-name QComTable \
  --time-to-live-specification "Enabled=true,AttributeName=TTL" \
  --region us-east-1
```

### 3. Configure Application

```bash
# No DYNAMODB_ENDPOINT needed for AWS
export DYNAMODB_REGION="us-east-1"
export DYNAMODB_TABLE_NAME="QComTable"
export JWT_SECRET_KEY="$(aws secretsmanager get-secret-value --secret-id jwt-secret --query SecretString --output text)"
```

### 4. IAM Permissions

```json
{
  "Version": "2012-10-17",
  "Statement": [
    {
      "Effect": "Allow",
      "Action": [
        "dynamodb:GetItem",
        "dynamodb:PutItem",
        "dynamodb:DeleteItem",
        "dynamodb:Query",
        "dynamodb:Scan",
        "dynamodb:UpdateItem"
      ],
      "Resource": "arn:aws:dynamodb:us-east-1:*:table/QComTable"
    }
  ]
}
```

## Benefits

### ✅ Simplified Architecture
- **Single Data Store**: Only DynamoDB, no separate cache
- **Fewer Dependencies**: No Redis client library
- **Less Configuration**: Fewer environment variables

### ✅ Operational Simplicity
- **No Cache Maintenance**: No Redis/Valkey cluster to manage
- **No Connection Issues**: No cache connectivity problems
- **Automatic Cleanup**: TTL handles expiration automatically

### ✅ AWS Native
- **Better Integration**: Native AWS service
- **Managed Service**: Fully managed by AWS
- **Auto-scaling**: Scales automatically with traffic
- **No EC2 Instances**: No need to manage cache servers

### ✅ Cost Effective
- **Pay Per Request**: Only pay for actual usage
- **No Cache Instances**: No separate cache infrastructure costs
- **Free TTL Deletions**: Automatic cleanup at no cost

## Testing

### Run Integration Tests

```bash
# Start Docker and run tests
make test-integration
```

### Manual Testing

```bash
# 1. Start DynamoDB
docker-compose up -d

# 2. Create table
./scripts/create-table.sh

# 3. Start server
export JWT_SECRET_KEY="test-secret-key-32-characters-long"
export DYNAMODB_ENDPOINT="http://localhost:8000"
./bin/qcom-server

# 4. Test OTP generation
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number": "+1234567890"}'

# 5. Check DynamoDB for OTP (should see TTL attribute)
aws dynamodb get-item \
  --table-name QComTable \
  --key '{"PK":{"S":"OTP#+1234567890"},"SK":{"S":"METADATA"}}' \
  --endpoint-url http://localhost:8000
```

## Monitoring TTL

### Check TTL Status

```bash
aws dynamodb describe-time-to-live \
  --table-name QComTable \
  --region us-east-1
```

### View Items with TTL

```bash
aws dynamodb scan \
  --table-name QComTable \
  --filter-expression "attribute_exists(#ttl)" \
  --expression-attribute-names '{"#ttl":"TTL"}' \
  --endpoint-url http://localhost:8000
```

## Performance

### DynamoDB Performance Characteristics

- **Read Latency**: Single-digit milliseconds
- **Write Latency**: Single-digit milliseconds
- **Throughput**: Scales to millions of requests per second
- **Consistency**: Strong consistency available

### Compared to Redis

| Feature | Redis | DynamoDB |
|---------|-------|----------|
| Latency | Sub-millisecond | Single-digit ms |
| Management | Self-managed | Fully managed |
| Scaling | Manual | Automatic |
| Persistence | Optional | Always persistent |
| Cost | Instance-based | Pay-per-request |

## Troubleshooting

### TTL Not Working

```bash
# Check if TTL is enabled
aws dynamodb describe-time-to-live --table-name QComTable

# Enable TTL if needed
aws dynamodb update-time-to-live \
  --table-name QComTable \
  --time-to-live-specification "Enabled=true,AttributeName=TTL"
```

### Items Not Expiring

- TTL deletions can take up to 48 hours
- Ensure TTL values are in Unix seconds (not milliseconds)
- Check that TTL attribute name matches ("TTL")

### Connection Issues

```bash
# Test DynamoDB connection
aws dynamodb list-tables --endpoint-url http://localhost:8000

# Check application logs
tail -f /tmp/server.log
```

## Migration from Redis

If you were using Redis before:

1. ✅ **No Data Migration Needed**: Start fresh with DynamoDB
2. ✅ **Remove Redis**: Stop and remove Redis containers
3. ✅ **Update Environment**: Remove REDIS_* environment variables
4. ✅ **Deploy**: Deploy new version with DynamoDB-only code

## Summary

The DynamoDB-only implementation provides:
- **Simpler architecture** with fewer moving parts
- **Better AWS integration** for cloud deployments
- **Automatic expiration** via TTL
- **No cache management** overhead
- **Production-ready** and scalable

Perfect for AWS deployments where you want to minimize operational complexity!

