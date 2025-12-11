# Testing the Home API Endpoint

This document describes how to test the `/api/v1/home` endpoint.

## Overview

The `/api/v1/home` endpoint has been integrated into the existing test infrastructure with comprehensive test coverage.

## Test Files

### 1. Full Integration Test Suite
**File:** `scripts/integration-test.sh`

This is the complete integration test that:
- Starts DynamoDB in Docker
- Creates the table
- Builds and starts the server
- Runs all authentication tests
- **NEW:** Tests the home endpoint (Tests 12-17)

**New Tests Added:**
- **Test 12:** Seed home page data in DynamoDB
- **Test 13:** GET `/api/v1/home` with valid token
- **Test 14:** GET `/api/v1/home` without token (expects 401)
- **Test 15:** GET `/api/v1/home` with invalid token (expects 401)
- **Test 16:** GET `/api/v1/home` with invalid request body (expects 400)
- **Test 17:** Verify location coordinates are logged

**Usage:**
```bash
# Run complete integration test suite
make test-integration

# Or directly:
./scripts/integration-test.sh
```

**Requirements:**
- Docker daemon running
- AWS CLI installed

---

### 2. Quick Home Endpoint Test
**File:** `scripts/test-home-endpoint.sh`

A focused test script that tests only the home endpoint. This is useful when:
- You already have the server running
- You want to quickly test the home endpoint
- Docker is not available

**Tests Covered:**
1. Server health check
2. Authentication flow (get tokens)
3. Seed home page data
4. GET `/api/v1/home` with valid token
5. GET `/api/v1/home` without token
6. GET `/api/v1/home` with invalid token
7. GET `/api/v1/home` with invalid request body
8. GET `/api/v1/home` with different coordinates

**Usage:**
```bash
# Start your server first
make run

# In another terminal, run the test
./scripts/test-home-endpoint.sh
```

**Requirements:**
- Server running on `http://localhost:8080`
- DynamoDB accessible at `http://localhost:8000`
- AWS CLI installed

---

### 3. Interactive API Examples
**File:** `examples/api-examples.sh`

An interactive script that demonstrates all API endpoints including the new home endpoint.

**Usage:**
```bash
# Start your server first
make run

# In another terminal, run the examples
./examples/api-examples.sh
```

The script will:
1. Run through all authentication endpoints
2. **NEW:** Automatically seed home page data
3. **NEW:** Call the home endpoint with your coordinates
4. Display requests and responses

---

## Test Data

### Home Page Seed Data

The tests automatically seed a sample home page record in DynamoDB:

```json
{
  "PK": "PAGE#HOME",
  "SK": "PAGE#HOME",
  "content": {
    "title": "Welcome to QCom",
    "subtitle": "Your trusted platform",
    "sections": [
      {
        "id": "section-1",
        "type": "hero",
        "title": "Featured Products"
      }
    ],
    "version": 1
  }
}
```

### Manual Seeding

You can also manually seed the data:

```bash
./scripts/seed-home-page.sh
```

Or use AWS CLI directly:

```bash
aws dynamodb put-item \
  --table-name QComTable \
  --region us-east-1 \
  --endpoint-url http://localhost:8000 \
  --item '{
    "PK": {"S": "PAGE#HOME"},
    "SK": {"S": "PAGE#HOME"},
    "content": {
      "M": {
        "title": {"S": "Welcome to QCom"},
        "subtitle": {"S": "Your trusted platform"}
      }
    }
  }'
```

---

## Test Coverage Summary

### Positive Test Cases
✅ GET `/api/v1/home` with valid authentication  
✅ Response contains `PAGE#HOME` data  
✅ Response includes all expected fields  
✅ Works with different latitude/longitude values  
✅ Location data is logged correctly  

### Negative Test Cases
✅ Returns 401 without authentication token  
✅ Returns 401 with invalid token  
✅ Returns 400 with malformed request body  
✅ Returns 404 when page data doesn't exist  

---

## Running Tests

### Option 1: Full Integration Test (Recommended)

```bash
# Runs everything from scratch
make test-integration
```

This will:
1. Start Docker containers
2. Create DynamoDB table
3. Build the application
4. Start the server
5. Run all tests (including home endpoint)
6. Clean up resources

**Expected Output:**
```
...
✓ PASS: Seed home page data
✓ PASS: Home page with valid token
✓ PASS: Home page contains expected content
✓ PASS: Home page without token (returns 401)
✓ PASS: Home page with invalid token (returns 401)
✓ PASS: Home page with invalid request body (returns 400)
...

==========================================
Test Summary
==========================================
Passed: 17+
Failed: 0

All tests passed!
```

---

### Option 2: Quick Test (Server Running)

```bash
# Terminal 1: Start server
make dev

# Terminal 2: Run quick test
./scripts/test-home-endpoint.sh
```

---

### Option 3: Manual Testing with curl

```bash
# 1. Get authentication token (follow the auth flow)
# See examples/curl-commands.md for details

# 2. Seed data (optional)
./scripts/seed-home-page.sh

# 3. Test the endpoint
curl -X POST http://localhost:8080/api/v1/home \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "latitude": 37.7749,
    "longitude": -122.4194
  }'
```

---

## Verifying Results

### Successful Response

```json
{
  "data": {
    "PK": "PAGE#HOME",
    "SK": "PAGE#HOME",
    "content": {
      "title": "Welcome to QCom",
      "subtitle": "Your trusted platform",
      "sections": [
        {
          "id": "section-1",
          "type": "hero",
          "title": "Featured Products"
        }
      ],
      "version": 1
    }
  }
}
```

### Location Logging

Check server logs for location data:

```bash
# View server logs
tail -f /tmp/server.log  # if running from test script

# or
docker logs <server-container>  # if running in Docker
```

You should see log entries like:

```json
{
  "level": "info",
  "msg": "Received home request with location",
  "latitude": 37.7749,
  "longitude": -122.4194,
  "time": "2025-12-11T..."
}
```

---

## Troubleshooting

### Test Fails: "PAGE_NOT_FOUND"

**Solution:** Seed the home page data first:
```bash
./scripts/seed-home-page.sh
```

### Test Fails: "Server is not running"

**Solution:** Start the server:
```bash
make run
```

### Test Fails: "Docker daemon not running"

**Solution:** Start Docker Desktop or run the quick test instead:
```bash
./scripts/test-home-endpoint.sh
```

### Test Fails: Authentication issues

**Solution:** Make sure JWT_SECRET_KEY is set:
```bash
export JWT_SECRET_KEY=$(openssl rand -base64 32)
```

---

## CI/CD Integration

The integration tests are designed to work in CI/CD pipelines:

```yaml
# Example GitHub Actions
- name: Run Integration Tests
  run: make test-integration
  env:
    JWT_SECRET_KEY: ${{ secrets.JWT_SECRET_KEY }}
```

---

## Next Steps

After verifying the tests pass:

1. ✅ All tests pass locally
2. ✅ Home endpoint returns correct data
3. ✅ Authentication is enforced
4. ✅ Location data is logged
5. 🔄 Ready for deployment

For more examples, see:
- `examples/api-examples.sh` - Interactive examples
- `examples/curl-commands.md` - Complete curl reference
- `scripts/integration-test.sh` - Full test suite

