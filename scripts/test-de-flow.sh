#!/usr/bin/env bash
# Manual curl test script for DE out-of-order onboarding flow.
# Run the server locally before executing: go run ./cmd/server
#
# Usage:
#   chmod +x scripts/test-de-flow.sh
#   ./scripts/test-de-flow.sh
#
# Or run individual steps by sourcing the file and calling functions directly.

set -euo pipefail

BASE_URL="${BASE_URL:-http://localhost:8080}"
PHONE="${PHONE:-+260971234567}"
OTP="112233"  # hardcoded dev OTP

separator() { echo; echo "────────────────────────────────────────"; echo "  $1"; echo "────────────────────────────────────────"; }

# ─── Step 1: Register DE ──────────────────────────────────────────────────────
separator "Step 1: Register DE"
REGISTER_RESP=$(curl -s -X POST "$BASE_URL/api/v1/de/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"name\": \"John Banda\",
    \"phone_number\": \"$PHONE\",
    \"profile_url\": \"https://s3.amazonaws.com/bunzo/de/john-profile.jpg\",
    \"nrc_url\": \"https://s3.amazonaws.com/bunzo/de/john-nrc.jpg\"
  }")
echo "$REGISTER_RESP" | python3 -m json.tool 2>/dev/null || echo "$REGISTER_RESP"

# ─── Step 2: Initiate OTP (as DE) ─────────────────────────────────────────────
separator "Step 2: Initiate OTP (X-App-Type: de)"
OTP_RESP=$(curl -s -X POST "$BASE_URL/api/v1/auth/initiate-otp" \
  -H "Content-Type: application/json" \
  -H "X-App-Type: de" \
  -d "{\"phone_number\": \"$PHONE\"}")
echo "$OTP_RESP" | python3 -m json.tool 2>/dev/null || echo "$OTP_RESP"
echo "(OTP is hardcoded: $OTP)"

# ─── Step 3: Verify OTP → get DE access token ─────────────────────────────────
separator "Step 3: Verify OTP → DE JWT"
VERIFY_RESP=$(curl -s -X POST "$BASE_URL/api/v1/auth/verify-otp" \
  -H "Content-Type: application/json" \
  -H "X-App-Type: de" \
  -d "{\"phone_number\": \"$PHONE\", \"otp\": \"$OTP\"}")
echo "$VERIFY_RESP" | python3 -m json.tool 2>/dev/null || echo "$VERIFY_RESP"

ACCESS_TOKEN=$(echo "$VERIFY_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['access_token'])" 2>/dev/null || true)
if [ -z "$ACCESS_TOKEN" ]; then
  echo "ERROR: Could not extract access_token. Aborting."
  exit 1
fi
echo "access_token: ${ACCESS_TOKEN:0:40}..."

# ─── Step 4: GET /de/me (should be offline) ───────────────────────────────────
separator "Step 4: GET /de/me → expect status: offline"
curl -s -X GET "$BASE_URL/api/v1/de/me" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  | python3 -m json.tool 2>/dev/null

# ─── Step 5: Get QR code for store 111 ───────────────────────────────────────
separator "Step 5: GET /stores/111/qr"
QR_RESP=$(curl -s -X GET "$BASE_URL/api/v1/stores/111/qr")
echo "$QR_RESP" | python3 -m json.tool 2>/dev/null || echo "$QR_RESP"

QR_CODE=$(echo "$QR_RESP" | python3 -c "import sys,json; print(json.load(sys.stdin)['qr_code'])" 2>/dev/null || true)
if [ -z "$QR_CODE" ]; then
  echo "ERROR: Could not extract qr_code. Aborting."
  exit 1
fi
echo "qr_code: $QR_CODE"

# ─── Step 6: Start duty (scan QR) ─────────────────────────────────────────────
separator "Step 6: POST /de/duty/start → expect status: eligible"
curl -s -X POST "$BASE_URL/api/v1/de/duty/start" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -d "{\"qr_code\": \"$QR_CODE\"}" \
  | python3 -m json.tool 2>/dev/null

# ─── Step 7: GET /de/me (should now be eligible) ──────────────────────────────
separator "Step 7: GET /de/me → expect status: eligible, store: 111"
curl -s -X GET "$BASE_URL/api/v1/de/me" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  | python3 -m json.tool 2>/dev/null

# ─── Step 8: Error cases ──────────────────────────────────────────────────────
separator "Step 8a: Start duty again → expect INVALID_STATE (already eligible)"
curl -s -X POST "$BASE_URL/api/v1/de/duty/start" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -d "{\"qr_code\": \"$QR_CODE\"}" \
  | python3 -m json.tool 2>/dev/null

separator "Step 8b: Expired QR → expect QR_EXPIRED"
curl -s -X POST "$BASE_URL/api/v1/de/duty/start" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -d '{"qr_code": "1112020010100"}' \
  | python3 -m json.tool 2>/dev/null

separator "Step 8c: Duplicate registration → expect 409 DE_ALREADY_EXISTS"
curl -s -X POST "$BASE_URL/api/v1/de/register" \
  -H "Content-Type: application/json" \
  -d "{
    \"name\": \"John Banda\",
    \"phone_number\": \"$PHONE\",
    \"profile_url\": \"https://s3.amazonaws.com/bunzo/de/john-profile.jpg\",
    \"nrc_url\": \"https://s3.amazonaws.com/bunzo/de/john-nrc.jpg\"
  }" | python3 -m json.tool 2>/dev/null

separator "Step 8d: Customer login (no X-App-Type header) → entity_type should be customer"
CUST_VERIFY=$(curl -s -X POST "$BASE_URL/api/v1/auth/initiate-otp" \
  -H "Content-Type: application/json" \
  -d "{\"phone_number\": \"$PHONE\"}" && \
  curl -s -X POST "$BASE_URL/api/v1/auth/verify-otp" \
  -H "Content-Type: application/json" \
  -d "{\"phone_number\": \"$PHONE\", \"otp\": \"$OTP\"}")
echo "$CUST_VERIFY" | python3 -m json.tool 2>/dev/null || echo "$CUST_VERIFY"

separator "Step 8e: DE endpoint with customer token → expect 403"
CUST_TOKEN=$(echo "$CUST_VERIFY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('access_token',''))" 2>/dev/null || true)
if [ -n "$CUST_TOKEN" ]; then
  curl -s -X GET "$BASE_URL/api/v1/de/me" \
    -H "Authorization: Bearer $CUST_TOKEN" \
    | python3 -m json.tool 2>/dev/null
fi

separator "Step 8f: OTP initiate for unregistered phone with X-App-Type: de → expect 404"
curl -s -X POST "$BASE_URL/api/v1/auth/initiate-otp" \
  -H "Content-Type: application/json" \
  -H "X-App-Type: de" \
  -d '{"phone_number": "+260999000000"}' \
  | python3 -m json.tool 2>/dev/null

separator "All steps done."
