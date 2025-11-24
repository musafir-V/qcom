# QCom Authentication API - curl Command Examples

This file contains sample curl commands to test the QCom authentication API manually.

## Prerequisites

- Server running on `http://localhost:8080`
- DynamoDB running
- `JWT_SECRET_KEY` environment variable set

## Base URL

```bash
BASE_URL="http://localhost:8080"
```

## 1. Health Check

```bash
curl -X GET http://localhost:8080/health
```

**Expected Response:**
```
OK
```

## 2. Initiate OTP

Request an OTP for a phone number.

```bash
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "+1234567890"
  }'
```

**Expected Response:**
```json
{
  "message": "OTP sent successfully"
}
```

**Note:** The OTP is logged in server logs for development/testing.

## 3. Verify OTP

Verify the OTP and get JWT tokens.

```bash
curl -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "+1234567890",
    "otp": "123456"
  }'
```

**Expected Response:**
```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 900,
  "user": {
    "phone_number": "+1234567890",
    "name": ""
  }
}
```

**Save the tokens for subsequent requests:**
```bash
# Extract tokens (example)
RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number":"+1234567890","otp":"123456"}')

ACCESS_TOKEN=$(echo "$RESPONSE" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
REFRESH_TOKEN=$(echo "$RESPONSE" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)
```

## 4. Get Current User (Protected Endpoint)

Get current user information using the access token.

```bash
curl -X GET http://localhost:8080/api/v1/me \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN"
```

**Example with extracted token:**
```bash
curl -X GET http://localhost:8080/api/v1/me \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected Response:**
```json
{
  "phone": "+1234567890"
}
```

## 5. Refresh Token

Get a new access token using the refresh token.

```bash
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "YOUR_REFRESH_TOKEN"
  }'
```

**Example with extracted token:**
```bash
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}"
```

**Expected Response:**
```json
{
  "access_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "refresh_token": "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9...",
  "token_type": "Bearer",
  "expires_in": 900
}
```

**Note:** The old refresh token is revoked when you refresh. Use the new tokens for subsequent requests.

## 6. Logout

Revoke the refresh token.

```bash
curl -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "YOUR_REFRESH_TOKEN"
  }'
```

**Example with extracted tokens:**
```bash
curl -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}"
```

**Expected Response:**
```json
{
  "message": "Logged out successfully"
}
```

After logout, the refresh token cannot be used to get new access tokens.

## Error Responses

All endpoints return errors in the following format:

```json
{
  "error": {
    "code": "ERROR_CODE",
    "message": "Human readable error message"
  }
}
```

### Common Error Codes

- `INVALID_REQUEST` - Invalid request body or parameters
- `INVALID_PHONE` - Invalid phone number format
- `INVALID_OTP` - Invalid or expired OTP
- `UNAUTHORIZED` - Missing or invalid authentication token
- `TOKEN_REVOKED` - Token has been revoked
- `OTP_GENERATION_FAILED` - Failed to generate OTP
- `TOKEN_GENERATION_FAILED` - Failed to generate tokens

## Complete Flow Example

Here's a complete flow from start to finish:

```bash
# 1. Health check
curl -X GET http://localhost:8080/health

# 2. Initiate OTP
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number":"+1234567890"}'

# 3. Get OTP from server logs (development only)
# Check server logs for the OTP

# 4. Verify OTP and get tokens
RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d "{\"phone_number\":\"+1234567890\",\"otp\":\"YOUR_OTP\"}")

# 5. Extract tokens
ACCESS_TOKEN=$(echo "$RESPONSE" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
REFRESH_TOKEN=$(echo "$RESPONSE" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)

# 6. Use protected endpoint
curl -X GET http://localhost:8080/api/v1/me \
  -H "Authorization: Bearer $ACCESS_TOKEN"

# 7. Refresh token
NEW_RESPONSE=$(curl -s -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$REFRESH_TOKEN\"}")

NEW_ACCESS_TOKEN=$(echo "$NEW_RESPONSE" | grep -o '"access_token":"[^"]*"' | cut -d'"' -f4)
NEW_REFRESH_TOKEN=$(echo "$NEW_RESPONSE" | grep -o '"refresh_token":"[^"]*"' | cut -d'"' -f4)

# 8. Logout
curl -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer $NEW_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"refresh_token\":\"$NEW_REFRESH_TOKEN\"}"
```

## Testing Invalid Scenarios

### Invalid Phone Number

```bash
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number":"invalid"}'
```

**Expected:** HTTP 400 with `INVALID_PHONE` error

### Invalid OTP

```bash
curl -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "+1234567890",
    "otp": "000000"
  }'
```

**Expected:** HTTP 401 with `INVALID_OTP` error

### Missing Authorization Header

```bash
curl -X GET http://localhost:8080/api/v1/me
```

**Expected:** HTTP 401 with `UNAUTHORIZED` error

### Invalid Access Token

```bash
curl -X GET http://localhost:8080/api/v1/me \
  -H "Authorization: Bearer invalid.token.here"
```

**Expected:** HTTP 401 with `UNAUTHORIZED` error

### Expired Refresh Token

```bash
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{"refresh_token":"expired.token.here"}'
```

**Expected:** HTTP 401 with `INVALID_TOKEN` error

