#!/bin/bash

# Quick integration test for /api/v1/home endpoint
# This assumes the server and DynamoDB are already running
# Usage: ./scripts/test-home-endpoint.sh

set +e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
BASE_URL="${BASE_URL:-http://localhost:8080}"
TEST_PHONE="+1234567890"

# Counters
PASSED=0
FAILED=0

# Helper functions
print_test() {
    echo -e "${YELLOW}Testing: $1${NC}"
}

print_pass() {
    echo -e "${GREEN}✓ PASS: $1${NC}"
    ((PASSED++))
}

print_fail() {
    echo -e "${RED}✗ FAIL: $1${NC}"
    ((FAILED++))
}

echo ""
echo "=========================================="
echo "Home Endpoint Integration Tests"
echo "=========================================="
echo ""
echo "Prerequisites:"
echo "  - Server running on $BASE_URL"
echo "  - DynamoDB accessible"
echo ""

# Test 1: Check if server is running
print_test "Server Health Check"
RESPONSE=$(curl -s -w "\n%{http_code}" $BASE_URL/health)
HTTP_CODE=$(echo "$RESPONSE" | tail -1)

if [ "$HTTP_CODE" == "200" ]; then
    print_pass "Server is running"
else
    print_fail "Server is not running (HTTP $HTTP_CODE)"
    echo "Please start the server first: make run"
    exit 1
fi

# Test 2: Get authentication tokens
print_test "Getting Authentication Tokens"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"$TEST_PHONE\"}" \
    $BASE_URL/api/v1/auth/initiate-otp 2>&1)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "200" ]; then
    print_pass "OTP initiated"
    
    # Wait a bit for logs to be written
    sleep 2
    
    # Ask user for OTP
    echo ""
    echo "Please check server logs for the OTP and enter it below:"
    read -p "Enter OTP: " OTP
    
    if [ -n "$OTP" ]; then
        # Verify OTP
        RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
            -H "Content-Type: application/json" \
            -d "{\"phone_number\":\"$TEST_PHONE\",\"otp\":\"$OTP\"}" \
            $BASE_URL/api/v1/auth/verify-otp)
        
        HTTP_CODE=$(echo "$RESPONSE" | tail -1)
        BODY=$(echo "$RESPONSE" | sed '$d')
        
        if [ "$HTTP_CODE" == "200" ]; then
            print_pass "OTP verified"
            ACCESS_TOKEN=$(echo "$BODY" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
        else
            print_fail "OTP verification failed (HTTP $HTTP_CODE)"
            exit 1
        fi
    else
        print_fail "No OTP provided"
        exit 1
    fi
else
    print_fail "OTP initiation failed (HTTP $HTTP_CODE)"
    exit 1
fi

if [ -z "$ACCESS_TOKEN" ]; then
    print_fail "Could not get access token"
    exit 1
fi

echo ""
echo "=========================================="
echo "Testing /api/v1/home Endpoint"
echo "=========================================="
echo ""

# Test 3: Seed Home Page Data (if not already present)
print_test "Seeding Home Page Data"
DYNAMODB_ENDPOINT="${DYNAMODB_ENDPOINT:-http://localhost:8000}"
DYNAMODB_TABLE_NAME="${DYNAMODB_TABLE_NAME:-QComTable}"

aws dynamodb put-item \
    --table-name "$DYNAMODB_TABLE_NAME" \
    --region us-east-1 \
    --endpoint-url "$DYNAMODB_ENDPOINT" \
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
                                "title": {"S": "Featured Products"}
                            }
                        }
                    ]
                },
                "version": {"N": "1"}
            }
        }
    }' > /dev/null 2>&1

if [ $? -eq 0 ]; then
    print_pass "Home page data seeded (or already exists)"
else
    echo "  Warning: Could not seed data, but continuing..."
fi

# Test 4: Home Page - With Valid Token
print_test "GET /api/v1/home - With Valid Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"latitude": 37.7749, "longitude": -122.4194}' \
    $BASE_URL/api/v1/home)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" == "200" ]; then
    print_pass "Home endpoint returns 200"
    
    # Check if response contains expected fields
    if echo "$BODY" | grep -q "PAGE#HOME"; then
        print_pass "Response contains PAGE#HOME"
    else
        print_fail "Response missing PAGE#HOME"
    fi
    
    if echo "$BODY" | grep -q "data"; then
        print_pass "Response contains data field"
    else
        print_fail "Response missing data field"
    fi
    
    echo ""
    echo "Response preview:"
    echo "$BODY" | head -c 200
    echo "..."
    echo ""
else
    print_fail "Home endpoint failed (HTTP $HTTP_CODE): $BODY"
fi

# Test 5: Home Page - Without Token
print_test "GET /api/v1/home - Without Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d '{"latitude": 37.7749, "longitude": -122.4194}' \
    $BASE_URL/api/v1/home)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Home endpoint requires authentication (returns 401)"
else
    print_fail "Home endpoint without token (HTTP $HTTP_CODE)"
fi

# Test 6: Home Page - With Invalid Token
print_test "GET /api/v1/home - With Invalid Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: Bearer invalid.token.here" \
    -H "Content-Type: application/json" \
    -d '{"latitude": 37.7749, "longitude": -122.4194}' \
    $BASE_URL/api/v1/home)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Home endpoint rejects invalid token (returns 401)"
else
    print_fail "Home endpoint with invalid token (HTTP $HTTP_CODE)"
fi

# Test 7: Home Page - Invalid Request Body
print_test "GET /api/v1/home - Invalid Request Body"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d 'invalid json' \
    $BASE_URL/api/v1/home)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "400" ]; then
    print_pass "Home endpoint validates request body (returns 400)"
else
    print_fail "Home endpoint with invalid body (HTTP $HTTP_CODE)"
fi

# Test 8: Home Page - With Different Coordinates
print_test "GET /api/v1/home - Different Coordinates"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Authorization: Bearer $ACCESS_TOKEN" \
    -H "Content-Type: application/json" \
    -d '{"latitude": 12.9716, "longitude": 77.5946}' \
    $BASE_URL/api/v1/home)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "200" ]; then
    print_pass "Home endpoint works with different coordinates"
else
    print_fail "Home endpoint with different coordinates (HTTP $HTTP_CODE)"
fi

# Print summary
echo ""
echo "=========================================="
echo "Test Summary"
echo "=========================================="
echo -e "${GREEN}Passed: $PASSED${NC}"
echo -e "${RED}Failed: $FAILED${NC}"
echo ""

if [ $FAILED -eq 0 ]; then
    echo -e "${GREEN}All tests passed!${NC}"
    exit 0
else
    echo -e "${RED}Some tests failed${NC}"
    exit 1
fi

