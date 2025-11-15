#!/bin/bash

# =============================================================================
# QCom Authentication API - Sample curl Commands
# =============================================================================
# This file contains sample curl commands to test the QCom authentication API
# 
# Prerequisites:
# - Server should be running on http://localhost:8080
# - DynamoDB and Redis should be running
# - Set JWT_SECRET_KEY environment variable
#
# Usage:
#   chmod +x examples/api-examples.sh
#   ./examples/api-examples.sh
#   Or copy individual commands to test manually
# =============================================================================

BASE_URL="http://localhost:8080"
PHONE_NUMBER="+1234567890"

echo "=========================================="
echo "QCom Authentication API Examples"
echo "=========================================="
echo ""

# =============================================================================
# 1. Health Check
# =============================================================================
echo "1. Health Check"
echo "---------------"
curl -X GET "${BASE_URL}/health"
echo -e "\n\n"

# =============================================================================
# 2. Initiate OTP
# =============================================================================
echo "2. Initiate OTP"
echo "---------------"
echo "Request:"
echo "curl -X POST ${BASE_URL}/api/v1/auth/initiate-otp \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{\"phone_number\":\"${PHONE_NUMBER}\"}'"
echo ""
echo "Response:"
RESPONSE=$(curl -s -X POST "${BASE_URL}/api/v1/auth/initiate-otp" \
  -H "Content-Type: application/json" \
  -d "{\"phone_number\":\"${PHONE_NUMBER}\"}")
echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
echo ""
echo "Note: Check server logs for the OTP (it's logged for development)"
echo -e "\n\n"

# =============================================================================
# 3. Verify OTP
# =============================================================================
echo "3. Verify OTP"
echo "-------------"
echo "First, get the OTP from server logs or Redis:"
echo "  redis-cli -h localhost -p 6379 get 'otp:plain:${PHONE_NUMBER}'"
echo ""
echo "Then verify with:"
echo "curl -X POST ${BASE_URL}/api/v1/auth/verify-otp \\"
echo "  -H 'Content-Type: application/json' \\"
echo "  -d '{"
echo "    \"phone_number\":\"${PHONE_NUMBER}\","
echo "    \"otp\":\"123456\""
echo "  }'"
echo ""
echo "Example (replace OTP with actual value):"
read -p "Enter OTP: " OTP
if [ -n "$OTP" ]; then
  RESPONSE=$(curl -s -X POST "${BASE_URL}/api/v1/auth/verify-otp" \
    -H "Content-Type: application/json" \
    -d "{\"phone_number\":\"${PHONE_NUMBER}\",\"otp\":\"${OTP}\"}")
  echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
  
  # Extract tokens for next examples
  ACCESS_TOKEN=$(echo "$RESPONSE" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
  REFRESH_TOKEN=$(echo "$RESPONSE" | grep -o '"refresh_token":"[^"]*"' | head -1 | cut -d'"' -f4)
  
  if [ -n "$ACCESS_TOKEN" ]; then
    echo ""
    echo "✓ Tokens extracted successfully"
    echo "Access Token (first 50 chars): ${ACCESS_TOKEN:0:50}..."
    echo "Refresh Token (first 50 chars): ${REFRESH_TOKEN:0:50}..."
  fi
else
  echo "Skipping OTP verification (no OTP provided)"
fi
echo -e "\n\n"

# =============================================================================
# 4. Get Current User (Protected Endpoint)
# =============================================================================
echo "4. Get Current User (Protected Endpoint)"
echo "-----------------------------------------"
if [ -n "$ACCESS_TOKEN" ]; then
  echo "Request:"
  echo "curl -X GET ${BASE_URL}/api/v1/me \\"
  echo "  -H 'Authorization: Bearer <access_token>'"
  echo ""
  echo "Response:"
  RESPONSE=$(curl -s -X GET "${BASE_URL}/api/v1/me" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}")
  echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
else
  echo "Skipping (no access token available)"
  echo "Example command:"
  echo "curl -X GET ${BASE_URL}/api/v1/me \\"
  echo "  -H 'Authorization: Bearer YOUR_ACCESS_TOKEN'"
fi
echo -e "\n\n"

# =============================================================================
# 5. Refresh Token
# =============================================================================
echo "5. Refresh Token"
echo "----------------"
if [ -n "$REFRESH_TOKEN" ]; then
  echo "Request:"
  echo "curl -X POST ${BASE_URL}/api/v1/auth/refresh \\"
  echo "  -H 'Content-Type: application/json' \\"
  echo "  -d '{\"refresh_token\":\"<refresh_token>\"}'"
  echo ""
  echo "Response:"
  RESPONSE=$(curl -s -X POST "${BASE_URL}/api/v1/auth/refresh" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${REFRESH_TOKEN}\"}")
  echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
  
  # Extract new tokens
  NEW_ACCESS_TOKEN=$(echo "$RESPONSE" | grep -o '"access_token":"[^"]*"' | head -1 | cut -d'"' -f4)
  NEW_REFRESH_TOKEN=$(echo "$RESPONSE" | grep -o '"refresh_token":"[^"]*"' | head -1 | cut -d'"' -f4)
  
  if [ -n "$NEW_ACCESS_TOKEN" ]; then
    echo ""
    echo "✓ New tokens received"
    ACCESS_TOKEN="$NEW_ACCESS_TOKEN"
    REFRESH_TOKEN="$NEW_REFRESH_TOKEN"
  fi
else
  echo "Skipping (no refresh token available)"
  echo "Example command:"
  echo "curl -X POST ${BASE_URL}/api/v1/auth/refresh \\"
  echo "  -H 'Content-Type: application/json' \\"
  echo "  -d '{\"refresh_token\":\"YOUR_REFRESH_TOKEN\"}'"
fi
echo -e "\n\n"

# =============================================================================
# 6. Logout
# =============================================================================
echo "6. Logout"
echo "---------"
if [ -n "$ACCESS_TOKEN" ] && [ -n "$REFRESH_TOKEN" ]; then
  echo "Request:"
  echo "curl -X POST ${BASE_URL}/api/v1/auth/logout \\"
  echo "  -H 'Authorization: Bearer <access_token>' \\"
  echo "  -H 'Content-Type: application/json' \\"
  echo "  -d '{\"refresh_token\":\"<refresh_token>\"}'"
  echo ""
  echo "Response:"
  RESPONSE=$(curl -s -X POST "${BASE_URL}/api/v1/auth/logout" \
    -H "Authorization: Bearer ${ACCESS_TOKEN}" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${REFRESH_TOKEN}\"}")
  echo "$RESPONSE" | jq '.' 2>/dev/null || echo "$RESPONSE"
  
  echo ""
  echo "Verifying refresh token is revoked..."
  RESPONSE2=$(curl -s -X POST "${BASE_URL}/api/v1/auth/refresh" \
    -H "Content-Type: application/json" \
    -d "{\"refresh_token\":\"${REFRESH_TOKEN}\"}")
  HTTP_CODE=$(echo "$RESPONSE2" | tail -1)
  if echo "$RESPONSE2" | grep -q "401\|UNAUTHORIZED"; then
    echo "✓ Refresh token successfully revoked"
  else
    echo "✗ Refresh token still valid (unexpected)"
  fi
else
  echo "Skipping (no tokens available)"
  echo "Example command:"
  echo "curl -X POST ${BASE_URL}/api/v1/auth/logout \\"
  echo "  -H 'Authorization: Bearer YOUR_ACCESS_TOKEN' \\"
  echo "  -H 'Content-Type: application/json' \\"
  echo "  -d '{\"refresh_token\":\"YOUR_REFRESH_TOKEN\"}'"
fi
echo -e "\n\n"

echo "=========================================="
echo "Examples Complete"
echo "=========================================="

