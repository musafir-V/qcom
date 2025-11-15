#!/bin/bash

# Don't exit on error - we want to run all tests
set +e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
BASE_URL="http://localhost:8080"
TEST_PHONE="+1234567890"
TEST_TABLE="QComTestTable"

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

check_response() {
    local response="$1"
    local expected_status="$2"
    local expected_text="$3"
    
    if echo "$response" | grep -q "\"code\""; then
        print_fail "Error in response: $response"
        return 1
    fi
    
    if [ -n "$expected_text" ]; then
        if echo "$response" | grep -q "$expected_text"; then
            return 0
        else
            print_fail "Expected text '$expected_text' not found in response"
            return 1
        fi
    fi
    
    return 0
}

# Cleanup function
cleanup() {
    echo ""
    echo "Cleaning up..."
    
    # Kill server if running
    if [ ! -z "$SERVER_PID" ]; then
        kill $SERVER_PID 2>/dev/null || true
        wait $SERVER_PID 2>/dev/null || true
    fi
    
    # Stop Docker containers
    docker stop qcom-dynamodb qcom-redis 2>/dev/null || true
    docker rm qcom-dynamodb qcom-redis 2>/dev/null || true
    
    echo "Cleanup complete"
}

# Trap to ensure cleanup on exit
trap cleanup EXIT

# Start Docker containers
echo "Starting Docker containers..."
# Stop and remove existing containers if they exist (clean start)
docker stop qcom-dynamodb qcom-redis 2>/dev/null || true
docker rm qcom-dynamodb qcom-redis 2>/dev/null || true

# Start DynamoDB
echo "Starting DynamoDB..."
if ! docker run -d --name qcom-dynamodb -p 8000:8000 \
    -e AWS_ACCESS_KEY_ID=dummy \
    -e AWS_SECRET_ACCESS_KEY=dummy \
    -e AWS_DEFAULT_REGION=us-east-1 \
    amazon/dynamodb-local:2.0.0 \
    -jar DynamoDBLocal.jar -sharedDb -inMemory; then
    echo "Failed to start DynamoDB container"
    exit 1
fi

# Start Redis
echo "Starting Redis..."
if ! docker run -d --name qcom-redis -p 6379:6379 \
    redis:7-alpine redis-server --appendonly yes; then
    echo "Failed to start Redis container"
    exit 1
fi

# Wait for services to be ready
echo "Waiting for services to be ready..."
for i in {1..15}; do
    if docker ps | grep -q "qcom-dynamodb" && docker ps | grep -q "qcom-redis"; then
        # Check if services are responding
        if curl -s http://localhost:8000 > /dev/null 2>&1 && redis-cli -h localhost -p 6379 ping > /dev/null 2>&1; then
            break
        fi
    fi
    sleep 1
done
sleep 2

# Create DynamoDB table
echo "Creating DynamoDB table..."
DYNAMODB_TABLE_NAME="$TEST_TABLE" ./scripts/create-table.sh > /dev/null 2>&1 || {
    # Table might already exist, try to delete and recreate
    echo "Table might exist, trying to delete and recreate..."
    aws dynamodb delete-table \
        --table-name "$TEST_TABLE" \
        --endpoint-url http://localhost:8000 \
        --region us-east-1 \
        > /dev/null 2>&1 || true
    sleep 3
    DYNAMODB_TABLE_NAME="$TEST_TABLE" ./scripts/create-table.sh > /dev/null 2>&1 || {
        echo "Warning: Failed to create table, but continuing..."
    }
}

# Generate JWT secret
JWT_SECRET=$(openssl rand -base64 32)

# Set environment variables
export JWT_SECRET_KEY="$JWT_SECRET"
export DYNAMODB_ENDPOINT="http://localhost:8000"
export DYNAMODB_REGION="us-east-1"
export DYNAMODB_TABLE_NAME="$TEST_TABLE"
export REDIS_ENDPOINT="localhost:6379"
export REDIS_PASSWORD=""
export REDIS_DB="0"
export PORT="8080"
export OTP_LENGTH="6"
export OTP_EXPIRY="10m"
export OTP_MAX_ATTEMPTS="5"

# Build the application
echo "Building application..."
if [ ! -d "cmd/server" ]; then
    echo "Error: cmd/server directory not found"
    exit 1
fi
go build -o /tmp/qcom-server ./cmd/server
if [ $? -ne 0 ]; then
    echo "Error: Failed to build application"
    exit 1
fi

# Start server in background
echo "Starting server..."
/tmp/qcom-server > /tmp/server.log 2>&1 &
SERVER_PID=$!

# Wait for server to be ready
echo "Waiting for server to be ready..."
for i in {1..30}; do
    if curl -s http://localhost:8080/health > /dev/null 2>&1; then
        echo "Server is ready!"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "Server failed to start"
        cat /tmp/server.log
        exit 1
    fi
    sleep 1
done

# Extract OTP from server logs (since OTP is logged)
get_otp_from_logs() {
    local phone="$1"
    sleep 2  # Wait for OTP to be generated and logged
    # Extract OTP from JSON logs - look for "OTP generated" log entry
    # The log format is JSON: {"level":"info","msg":"OTP generated (logged for development)","otp":"123456","phone":"+1234567890","time":"..."}
    # Try to extract from JSON log
    local otp_from_log=$(grep -o "\"otp\":\"[0-9]*\"" /tmp/server.log 2>/dev/null | tail -1 | grep -o '[0-9]*' || echo "")
    if [ -n "$otp_from_log" ]; then
        echo "$otp_from_log"
        return 0
    fi
    # Fallback: try Redis directly
    local otp_from_redis=$(redis-cli -h localhost -p 6379 get "otp:plain:$phone" 2>/dev/null | tr -d '"' || echo "")
    if [ -n "$otp_from_redis" ]; then
        echo "$otp_from_redis"
        return 0
    fi
    echo ""
    return 1
}

echo ""
echo "=========================================="
echo "Running Integration Tests"
echo "=========================================="
echo ""

# Test 1: Health Check
print_test "Health Check"
RESPONSE=$(curl -s -w "\n%{http_code}" http://localhost:8080/health)
HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "OK"; then
    print_pass "Health check"
else
    print_fail "Health check (HTTP $HTTP_CODE)"
fi

# Test 2: Initiate OTP
print_test "Initiate OTP"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"$TEST_PHONE\"}" \
    http://localhost:8080/api/v1/auth/initiate-otp 2>&1)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
BODY=$(echo "$RESPONSE" | sed '$d')

if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "OTP sent successfully"; then
    print_pass "Initiate OTP"
    OTP=$(get_otp_from_logs "$TEST_PHONE" 2>/dev/null || echo "")
    if [ -z "$OTP" ]; then
        echo "Warning: Could not extract OTP from logs, will skip OTP verification tests"
    else
        echo "  Extracted OTP: $OTP"
    fi
else
    print_fail "Initiate OTP (HTTP $HTTP_CODE): $BODY"
    OTP=""
fi

# Test 3: Initiate OTP with Invalid Phone
print_test "Initiate OTP - Invalid Phone"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"invalid\"}" \
    http://localhost:8080/api/v1/auth/initiate-otp)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "400" ]; then
    print_pass "Initiate OTP with invalid phone"
else
    print_fail "Initiate OTP with invalid phone (HTTP $HTTP_CODE)"
fi

# Test 4: Verify OTP
if [ -n "$OTP" ] && [ ${#OTP} -eq 6 ]; then
    print_test "Verify OTP"
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d "{\"phone_number\":\"$TEST_PHONE\",\"otp\":\"$OTP\"}" \
        http://localhost:8080/api/v1/auth/verify-otp)
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "access_token"; then
        print_pass "Verify OTP"
        # Extract tokens
        ACCESS_TOKEN=$(echo "$BODY" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
        REFRESH_TOKEN=$(echo "$BODY" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)
    else
        print_fail "Verify OTP (HTTP $HTTP_CODE): $BODY"
        ACCESS_TOKEN=""
        REFRESH_TOKEN=""
    fi
else
    print_fail "Verify OTP - Could not extract OTP from logs"
    ACCESS_TOKEN=""
    REFRESH_TOKEN=""
fi

# Test 5: Verify OTP with Invalid OTP
print_test "Verify OTP - Invalid OTP"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"$TEST_PHONE\",\"otp\":\"000000\"}" \
    http://localhost:8080/api/v1/auth/verify-otp)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Verify OTP with invalid OTP"
else
    print_fail "Verify OTP with invalid OTP (HTTP $HTTP_CODE)"
fi

# Test 6: Protected Endpoint (with valid token)
if [ -n "$ACCESS_TOKEN" ]; then
    print_test "Protected Endpoint - Valid Token"
    RESPONSE=$(curl -s -w "\n%{http_code}" -X GET \
        -H "Authorization: Bearer $ACCESS_TOKEN" \
        http://localhost:8080/api/v1/me)
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "$TEST_PHONE"; then
        print_pass "Protected endpoint with valid token"
    else
        print_fail "Protected endpoint with valid token (HTTP $HTTP_CODE): $BODY"
    fi
fi

# Test 7: Protected Endpoint (without token)
print_test "Protected Endpoint - No Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X GET \
    http://localhost:8080/api/v1/me)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Protected endpoint without token"
else
    print_fail "Protected endpoint without token (HTTP $HTTP_CODE)"
fi

# Test 8: Protected Endpoint (with invalid token)
print_test "Protected Endpoint - Invalid Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X GET \
    -H "Authorization: Bearer invalid.token.here" \
    http://localhost:8080/api/v1/me)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Protected endpoint with invalid token"
else
    print_fail "Protected endpoint with invalid token (HTTP $HTTP_CODE)"
fi

# Test 9: Refresh Token
if [ -n "$REFRESH_TOKEN" ]; then
    print_test "Refresh Token"
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}" \
        http://localhost:8080/api/v1/auth/refresh)
    
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "access_token"; then
        print_pass "Refresh token"
        NEW_ACCESS_TOKEN=$(echo "$BODY" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
        NEW_REFRESH_TOKEN=$(echo "$BODY" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)
        
        # Verify new access token works
        if [ -n "$NEW_ACCESS_TOKEN" ]; then
            RESPONSE2=$(curl -s -w "\n%{http_code}" -X GET \
                -H "Authorization: Bearer $NEW_ACCESS_TOKEN" \
                http://localhost:8080/api/v1/me)
            HTTP_CODE2=$(echo "$RESPONSE2" | tail -1)
            if [ "$HTTP_CODE2" == "200" ]; then
                print_pass "New access token from refresh works"
            else
                print_fail "New access token from refresh (HTTP $HTTP_CODE2)"
            fi
        fi
    else
        print_fail "Refresh token (HTTP $HTTP_CODE): $BODY"
        NEW_ACCESS_TOKEN=""
        NEW_REFRESH_TOKEN=""
    fi
fi

# Test 10: Refresh Token with Invalid Token
print_test "Refresh Token - Invalid Token"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"invalid.token\"}" \
    http://localhost:8080/api/v1/auth/refresh)

HTTP_CODE=$(echo "$RESPONSE" | tail -1)
if [ "$HTTP_CODE" == "401" ]; then
    print_pass "Refresh token with invalid token"
else
    print_fail "Refresh token with invalid token (HTTP $HTTP_CODE)"
fi

# Test 11: Logout
# Get fresh tokens for logout test (don't reuse tokens from refresh test)
LOGOUT_PHONE="+1999999999"
print_test "Logout - Getting fresh tokens"
RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"$LOGOUT_PHONE\"}" \
    http://localhost:8080/api/v1/auth/initiate-otp 2>&1)
sleep 2
LOGOUT_OTP=$(get_otp_from_logs "$LOGOUT_PHONE" 2>/dev/null || echo "")

if [ -n "$LOGOUT_OTP" ]; then
    # Verify OTP to get tokens
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
        -H "Content-Type: application/json" \
        -d "{\"phone_number\":\"$LOGOUT_PHONE\",\"otp\":\"$LOGOUT_OTP\"}" \
        http://localhost:8080/api/v1/auth/verify-otp)
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    
    if [ "$HTTP_CODE" == "200" ]; then
        # Extract tokens - try multiple methods
        LOGOUT_ACCESS_TOKEN=$(echo "$BODY" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
        LOGOUT_REFRESH_TOKEN=$(echo "$BODY" | grep -o '"refresh_token":"[^"]*"' | head -1 | cut -d'"' -f4)
        
        # If still empty, try with sed
        if [ -z "$LOGOUT_ACCESS_TOKEN" ]; then
            LOGOUT_ACCESS_TOKEN=$(echo "$BODY" | sed -n 's/.*"access_token":"\([^"]*\)".*/\1/p')
        fi
        if [ -z "$LOGOUT_REFRESH_TOKEN" ]; then
            LOGOUT_REFRESH_TOKEN=$(echo "$BODY" | sed -n 's/.*"refresh_token":"\([^"]*\)".*/\1/p')
        fi
        
        # Debug output
        if [ -z "$LOGOUT_ACCESS_TOKEN" ] || [ -z "$LOGOUT_REFRESH_TOKEN" ]; then
            echo "  Warning: Token extraction may have failed. Response: ${BODY:0:100}"
        fi
    else
        echo "  Warning: Verify OTP failed with HTTP $HTTP_CODE: ${BODY:0:100}"
    fi
fi

if [ -n "$LOGOUT_ACCESS_TOKEN" ] && [ -n "$LOGOUT_REFRESH_TOKEN" ]; then
    print_test "Logout"
    # Debug: Check token length
    if [ ${#LOGOUT_ACCESS_TOKEN} -lt 10 ]; then
        print_fail "Logout - Access token seems invalid (too short)"
    else
        RESPONSE=$(curl -s -w "\n%{http_code}" -X POST \
            -H "Authorization: Bearer $LOGOUT_ACCESS_TOKEN" \
            -H "Content-Type: application/json" \
            -d "{\"refresh_token\":\"$LOGOUT_REFRESH_TOKEN\"}" \
            http://localhost:8080/api/v1/auth/logout)
        
        HTTP_CODE=$(echo "$RESPONSE" | tail -1)
        BODY=$(echo "$RESPONSE" | sed '$d')
        
        if [ "$HTTP_CODE" == "200" ] && echo "$BODY" | grep -q "Logged out successfully"; then
            print_pass "Logout"
            
            # Verify refresh token is revoked
            RESPONSE2=$(curl -s -w "\n%{http_code}" -X POST \
                -H "Content-Type: application/json" \
                -d "{\"refresh_token\":\"$LOGOUT_REFRESH_TOKEN\"}" \
                http://localhost:8080/api/v1/auth/refresh)
            HTTP_CODE2=$(echo "$RESPONSE2" | tail -1)
            if [ "$HTTP_CODE2" == "401" ]; then
                print_pass "Refresh token revoked after logout"
            else
                print_fail "Refresh token not revoked after logout (HTTP $HTTP_CODE2)"
            fi
        else
            print_fail "Logout (HTTP $HTTP_CODE): $BODY"
        fi
    fi
else
    print_fail "Logout - Could not get tokens for logout test"
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

