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

## 7. Home Page API (Protected Endpoint)

Get home page data with user's location.

```bash
curl -X POST http://localhost:8080/api/v1/home \
  -H "Authorization: Bearer YOUR_ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "latitude": 37.7749,
    "longitude": -122.4194
  }'
```

**Example with extracted token:**
```bash
curl -X POST http://localhost:8080/api/v1/home \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "latitude": 37.7749,
    "longitude": -122.4194
  }'
```

**Expected Response:**
```json
{
  "data": {
    "PK": "PAGE#HOME",
    "SK": "PAGE#HOME",
    "content": {
      "...": "page content as stored in DynamoDB"
    }
  }
}
```

**Note:** 
- The latitude and longitude are logged but not currently used for filtering
- The API queries DynamoDB for the partition key `PAGE#HOME`
- The response contains the raw JSON data stored in DynamoDB for that key

## 8. Create Address (Protected Endpoint)

Save a new delivery address for the authenticated user.

```bash
curl -X POST http://localhost:8080/api/v1/addresses \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "receiver_name": "Shivang Awasthi",
    "receiver_phone": "+919876543210",
    "building_and_floor": "Tower B, 4th Floor, Flat 402",
    "address_line_1": "Sector 62, Noida",
    "address_line_2": "Near City Centre Metro Station",
    "latitude": 28.627235,
    "longitude": 77.364715,
    "tag": "home"
  }'
```

**Expected Response (201 Created):**
```json
{
  "data": {
    "address_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "user_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
    "receiver_name": "Shivang Awasthi",
    "receiver_phone": "+919876543210",
    "building_and_floor": "Tower B, 4th Floor, Flat 402",
    "address_line_1": "Sector 62, Noida",
    "address_line_2": "Near City Centre Metro Station",
    "latitude": 28.627235,
    "longitude": 77.364715,
    "tag": "home",
    "is_active": true,
    "created_at": "2026-04-04T10:30:00Z",
    "updated_at": "2026-04-04T10:30:00Z"
  }
}
```

**Save the address_id for subsequent requests:**
```bash
ADDRESS_ID=$(echo "$RESPONSE" | grep -o '"address_id":"[^"]*"' | cut -d'"' -f4)
```

## 9. Get Address by ID (Protected Endpoint)

Retrieve a single address. Ownership is verified via JWT.

```bash
curl -X GET http://localhost:8080/api/v1/addresses/$ADDRESS_ID \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected Response (200 OK):**
```json
{
  "data": {
    "address_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
    "receiver_name": "Shivang Awasthi",
    "receiver_phone": "+919876543210",
    "building_and_floor": "Tower B, 4th Floor, Flat 402",
    "address_line_1": "Sector 62, Noida",
    "address_line_2": "Near City Centre Metro Station",
    "latitude": 28.627235,
    "longitude": 77.364715,
    "tag": "home",
    "is_active": true
  }
}
```

## 10. Get All My Addresses (Protected Endpoint)

Retrieve all active addresses for the authenticated user.

```bash
curl -X GET http://localhost:8080/api/v1/addresses \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected Response (200 OK):**
```json
{
  "data": [
    {
      "address_id": "a1b2c3d4-...",
      "receiver_name": "Shivang Awasthi",
      "building_and_floor": "Tower B, 4th Floor, Flat 402",
      "tag": "home",
      "is_active": true
    }
  ],
  "pagination": {
    "count": 1,
    "next_token": null
  }
}
```

## 11. Suggest Nearby Addresses (Protected Endpoint)

Get saved addresses within 100 meters of the given coordinates, sorted nearest first.

```bash
curl -X GET "http://localhost:8080/api/v1/addresses/suggest?latitude=28.627235&longitude=77.364715" \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected Response (200 OK):**
```json
{
  "data": [
    {
      "address_id": "a1b2c3d4-...",
      "receiver_name": "Shivang Awasthi",
      "building_and_floor": "Tower B, 4th Floor, Flat 402",
      "latitude": 28.627235,
      "longitude": 77.364715,
      "distance_meters": 0.0,
      "is_active": true
    }
  ],
  "count": 1
}
```

**Note:** Returns an empty list (not 404) if no addresses are within 100 meters.

## 12. Update Receiver Details (Protected Endpoint)

Update only the receiver name and/or phone on an existing address. Location fields cannot be changed — create a new address instead.

```bash
curl -X PATCH http://localhost:8080/api/v1/addresses/$ADDRESS_ID \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "receiver_name": "Rahul Sharma",
    "receiver_phone": "+911234567890"
  }'
```

**Expected Response (200 OK):**
```json
{
  "data": {
    "address_id": "a1b2c3d4-...",
    "receiver_name": "Rahul Sharma",
    "receiver_phone": "+911234567890",
    "building_and_floor": "Tower B, 4th Floor, Flat 402",
    "is_active": true
  }
}
```

## 13. Remove Address — Soft Delete (Protected Endpoint)

Deactivate an address. Data is kept in DynamoDB for historical order references.

```bash
curl -X DELETE http://localhost:8080/api/v1/addresses/$ADDRESS_ID \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected Response (200 OK):**
```json
{
  "message": "Address removed successfully"
}
```

**Note:** The address is not physically deleted. It is marked as `is_active: false` and will no longer appear in listings or suggestions.

## Address API Error Scenarios

### Missing Required Field

```bash
curl -X POST http://localhost:8080/api/v1/addresses \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"address_line_1": "Street 1", "latitude": 28.0, "longitude": 77.0}'
```

**Expected:** HTTP 400 with `MISSING_FIELD` error

### Invalid Receiver Phone

```bash
curl -X POST http://localhost:8080/api/v1/addresses \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "receiver_name": "Test",
    "receiver_phone": "not-a-phone",
    "building_and_floor": "House 1",
    "address_line_1": "Street 1",
    "latitude": 28.0,
    "longitude": 77.0
  }'
```

**Expected:** HTTP 400 with `INVALID_PHONE` error

### Address Not Found

```bash
curl -X GET http://localhost:8080/api/v1/addresses/00000000-0000-0000-0000-000000000000 \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected:** HTTP 404 with `ADDRESS_NOT_FOUND` error

### Access Another User's Address

```bash
# Using User A's token to access User B's address
curl -X GET http://localhost:8080/api/v1/addresses/$OTHER_USERS_ADDRESS_ID \
  -H "Authorization: Bearer $ACCESS_TOKEN"
```

**Expected:** HTTP 403 with `FORBIDDEN` error

## 14. Check Serviceability (Protected Endpoint)

Given the customer's current coordinates, checks whether the location falls
inside a darkstore's serviceable-area polygon and resolves an address for it.
Seed darkstores first with `./scripts/seed-darkstores.sh`.

```bash
curl -X POST http://localhost:8080/api/v1/serviceability \
  -H "Authorization: Bearer $ACCESS_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "latitude": 12.9719,
    "longitude": 77.6412
  }'
```

**Serviceable — resolved from a saved address within 50 m (200 OK):**
```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "DS-001",
    "resolved_address": {
      "address_line": "Near City Centre Metro Station",
      "tag": "home",
      "address_id": "a1b2c3d4-e5f6-7890-abcd-ef1234567890",
      "source": "saved_address"
    }
  }
}
```

**Serviceable — resolved via Google reverse geocoding (200 OK):**
```json
{
  "data": {
    "serviceable": true,
    "darkstore_id": "DS-001",
    "resolved_address": {
      "address_line": "Indiranagar, Bengaluru",
      "tag": null,
      "address_id": null,
      "source": "geocoded"
    }
  }
}
```

**Unserviceable — coordinate is inside no darkstore polygon (200 OK):**
```json
{
  "data": {
    "serviceable": false
  }
}
```

**Note:** If `GOOGLE_MAPS_API_KEY` is unset or the Google call fails, a
serviceable location is still returned, just with `resolved_address: null`.

