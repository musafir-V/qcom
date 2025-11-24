# Integration Test Instructions

## Prerequisites

Docker Desktop must be running to execute integration tests.

## Steps to Run Integration Tests

### 1. Start Docker Desktop

**On macOS:**
- Open Finder → Applications → Docker
- Click on Docker.app
- Wait for Docker to start (whale icon in menu bar should be stable)

**Or via Terminal:**
```bash
open -a Docker
```

Wait 30-60 seconds for Docker to fully start.

### 2. Verify Docker is Running

```bash
docker ps
```

You should see a table header (even if empty). If you see "Cannot connect to the Docker daemon", Docker is not running yet.

### 3. Run Integration Tests

```bash
cd /Users/shivang.awasthi/Desktop/qcom
make test-integration
```

## What the Integration Tests Do

The test suite will:

1. **Start Docker Containers**
   - DynamoDB Local (port 8000)
   - Redis (port 6379)

2. **Create DynamoDB Table**
   - Creates `QComTable` with proper schema

3. **Start Application Server**
   - Runs on port 8080
   - Connects to DynamoDB and Redis

4. **Run Test Suite**
   - ✅ Health check endpoint
   - ✅ OTP initiation (tests Redis write)
   - ✅ OTP verification (tests Redis read)
   - ✅ Invalid OTP handling
   - ✅ JWT token generation
   - ✅ Protected endpoint access
   - ✅ Token refresh (tests Redis token storage)
   - ✅ Logout functionality

5. **Cleanup**
   - Stops and removes containers
   - Cleans up test data

## Expected Output

```
==========================================
Running Integration Tests
==========================================

Testing: Health Check
✓ PASS: Health check

Testing: Initiate OTP
✓ PASS: Initiate OTP
  Extracted OTP: 123456

Testing: Verify OTP
✓ PASS: Verify OTP

Testing: Protected Endpoint - Valid Token
✓ PASS: Protected endpoint with valid token

Testing: Refresh Token
✓ PASS: Refresh token

Testing: Logout
✓ PASS: Logout

==========================================
Test Summary
==========================================
Passed: 11
Failed: 0
```

## Manual Testing (if Docker is unavailable)

If you cannot run Docker, you can test manually:

### 1. Install Redis Locally

**macOS:**
```bash
brew install redis
redis-server
```

**Ubuntu:**
```bash
sudo apt-get install redis-server
sudo systemctl start redis
```

### 2. Install AWS CLI and Start DynamoDB Local

```bash
# Download DynamoDB Local
mkdir -p ~/dynamodb-local
cd ~/dynamodb-local
wget https://s3.us-west-2.amazonaws.com/dynamodb-local/dynamodb_local_latest.tar.gz
tar -xzf dynamodb_local_latest.tar.gz

# Start DynamoDB
java -Djava.library.path=./DynamoDBLocal_lib -jar DynamoDBLocal.jar -sharedDb -inMemory
```

### 3. Create DynamoDB Table

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
    --endpoint-url http://localhost:8000 \
    --region us-east-1
```

### 4. Start the Application

```bash
export JWT_SECRET_KEY="test-secret-key-32-characters-long"
export REDIS_ENDPOINT="localhost:6379"
export REDIS_PASSWORD=""
export DYNAMODB_ENDPOINT="http://localhost:8000"
export DYNAMODB_TABLE_NAME="QComTable"
export PORT="8080"

./bin/qcom-server
```

### 5. Test Manually

```bash
# Test health
curl http://localhost:8080/health

# Test OTP initiation
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number": "+1234567890"}'

# Check server logs for OTP, then verify
curl -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number": "+1234567890", "otp": "YOUR_OTP_FROM_LOGS"}'
```

## Troubleshooting

### Docker Not Starting
```bash
# Check Docker status
docker info

# Restart Docker
killall Docker && open -a Docker
```

### Port Already in Use
```bash
# Check what's using ports 6379 or 8000
lsof -i :6379
lsof -i :8000

# Kill process if needed
kill -9 <PID>
```

### DynamoDB Connection Error
```bash
# Verify DynamoDB is running
curl http://localhost:8000

# Check table exists
aws dynamodb list-tables \
    --endpoint-url http://localhost:8000 \
    --region us-east-1
```

### Redis Connection Error
```bash
# Test Redis connection
redis-cli ping

# Check Redis logs
docker logs qcom-redis
```

## Quick Test Command

Once Docker is running, just run:
```bash
make test-integration
```

This single command handles everything automatically!

