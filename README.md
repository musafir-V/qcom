# QCom Authentication Service

A Go-based authentication service with OTP verification via phone number and JWT token management using HS256 algorithm.

## Features

- Phone number-based authentication
- OTP generation and verification (logged for development)
- JWT access and refresh tokens (HS256)
- DynamoDB for user storage
- Redis for OTP and token storage (with password authentication)
- Optional TLS encryption for secure connections
- RESTful API with proper HTTP standards

## Architecture

```
┌─────────────┐    ┌──────────────┐    ┌─────────────┐    ┌─────────────┐
│   Client    │───▶│  Go Server   │───▶│  DynamoDB   │    │   Valkey    │
└─────────────┘    └──────────────┘    └─────────────┘    └─────────────┘
```

## API Endpoints

### Authentication

| Method | Endpoint | Description | Auth Required |
|--------|----------|-------------|---------------|
| `POST` | `/api/v1/auth/initiate-otp` | Request OTP for phone number | No |
| `POST` | `/api/v1/auth/verify-otp` | Verify OTP and get tokens | No |
| `POST` | `/api/v1/auth/refresh` | Refresh access token | No |
| `POST` | `/api/v1/auth/logout` | Revoke refresh token | Yes |
| `GET` | `/api/v1/me` | Get current user info | Yes |
| `GET` | `/health` | Health check | No |

## Quick Start

### Prerequisites

- Go 1.23+
- Docker and Docker Compose
- AWS CLI (for table creation)
- curl (for testing)

### Using Makefile (Recommended)

The easiest way to work with this project is using the Makefile:

```bash
# Show all available commands
make help

# Build the application
make build

# Setup development environment (start containers, create table)
make setup

# Run the application
make run

# Run integration tests (builds, starts dependencies, runs tests)
make test-integration

# Start development environment and run server
make dev
```

### Manual Setup

#### 1. Start Dependencies

```bash
make docker-up
# or
docker-compose up -d
```

#### 2. Create DynamoDB Table

```bash
make setup
# or manually:
chmod +x scripts/create-table.sh
./scripts/create-table.sh
```

#### 3. Generate JWT Secret Key

```bash
# Generate a secure 32-byte key
openssl rand -base64 32
```

#### 4. Set Environment Variables

```bash
export JWT_SECRET_KEY="<your-generated-key>"
export DYNAMODB_ENDPOINT="http://localhost:8000"
export DYNAMODB_REGION="us-east-1"
export DYNAMODB_TABLE_NAME="QComTable"
export REDIS_ENDPOINT="localhost:6379"
export PORT="8080"
```

#### 5. Run the Server

```bash
make run
# or
go run cmd/server/main.go
```

## Makefile Commands

| Command | Description |
|---------|-------------|
| `make help` | Show all available commands |
| `make build` | Build the application |
| `make run` | Run the application |
| `make test-integration` | Build, start dependencies, and run integration tests |
| `make setup` | Setup development environment (start containers, create table) |
| `make docker-up` | Start Docker containers |
| `make docker-down` | Stop Docker containers |
| `make docker-restart` | Restart Docker containers |
| `make dev` | Start development environment and run server |
| `make dev-test` | Start server with local containers for manual API testing |
| `make clean` | Clean build artifacts |
| `make clean-all` | Clean everything including Docker containers |
| `make deps` | Download Go dependencies |
| `make test` | Run unit tests |
| `make fmt` | Format code |
| `make vet` | Run go vet |
| `make lint` | Run linter |
| `make check` | Run all checks (format, vet, lint) |

## Integration Tests

Run the integration test script that:
1. Starts Docker containers (DynamoDB, Redis/Valkey)
2. Creates the DynamoDB table
3. Starts the application
4. Tests all API endpoints using curl
5. Validates responses

```bash
make test-integration
# or manually:
chmod +x scripts/integration-test.sh
./scripts/integration-test.sh
```

The script will:
- Test health check
- Test OTP initiation
- Test OTP verification
- Test protected endpoints
- Test token refresh
- Test logout
- Clean up resources

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | Server port |
| `JWT_SECRET_KEY` | (required) | Secret key for JWT signing (min 32 bytes) |
| `JWT_ACCESS_EXPIRY` | `15m` | Access token expiration |
| `JWT_REFRESH_EXPIRY` | `168h` | Refresh token expiration (7 days) |
| `DYNAMODB_ENDPOINT` | `` | DynamoDB endpoint (empty for AWS) |
| `DYNAMODB_REGION` | `us-east-1` | AWS region |
| `DYNAMODB_TABLE_NAME` | `QComTable` | DynamoDB table name |
| `REDIS_ENDPOINT` | `localhost:6379` | Redis endpoint |
| `REDIS_PASSWORD` | `` | Redis password |
| `REDIS_DB` | `0` | Redis database number |
| `REDIS_USE_TLS` | `false` | Enable TLS encryption |
| `OTP_LENGTH` | `6` | OTP length |
| `OTP_EXPIRY` | `10m` | OTP expiration |
| `OTP_MAX_ATTEMPTS` | `5` | Max OTP verification attempts |

## API Usage Examples

For detailed curl command examples, see:
- **Interactive script:** `examples/api-examples.sh` - Run this script to test all endpoints interactively
- **Command reference:** `examples/curl-commands.md` - Complete reference with all curl commands

### Quick Examples

### 1. Initiate OTP

```bash
curl -X POST http://localhost:8080/api/v1/auth/initiate-otp \
  -H "Content-Type: application/json" \
  -d '{"phone_number": "+1234567890"}'
```

Response:
```json
{
  "message": "OTP sent successfully"
}
```

**Note:** OTP is logged in server logs for development.

### 2. Verify OTP

```bash
curl -X POST http://localhost:8080/api/v1/auth/verify-otp \
  -H "Content-Type: application/json" \
  -d '{
    "phone_number": "+1234567890",
    "otp": "123456"
  }'
```

Response:
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

### 3. Use Access Token

```bash
curl -X GET http://localhost:8080/api/v1/me \
  -H "Authorization: Bearer <access_token>"
```

### 4. Refresh Token

```bash
curl -X POST http://localhost:8080/api/v1/auth/refresh \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "<refresh_token>"
  }'
```

### 5. Logout

```bash
curl -X POST http://localhost:8080/api/v1/auth/logout \
  -H "Authorization: Bearer <access_token>" \
  -H "Content-Type: application/json" \
  -d '{
    "refresh_token": "<refresh_token>"
  }'
```

## DynamoDB Schema

### User Table

**Partition Key (PK):** `USER!<phoneNumber>`  
**Sort Key (SK):** `METADATA`

**Attributes:**
- `phone_number` (String): Phone number in E.164 format
- `name` (String): User's name (optional)
- `created_at` (String): ISO 8601 timestamp
- `updated_at` (String): ISO 8601 timestamp

## Security Features

- **HS256 JWT Signing:** Symmetric HMAC-SHA256 algorithm
- **Token Rotation:** Refresh tokens are rotated on each use
- **Token Revocation:** Refresh tokens can be revoked
- **OTP Hashing:** OTPs are hashed with bcrypt before storage
- **Rate Limiting:** OTP attempts are limited
- **Secure Storage:** Tokens stored in Valkey with expiration

## Development

### Project Structure

```
.
├── cmd/
│   └── server/
│       └── main.go          # Application entry point
├── internal/
│   ├── config/               # Configuration management
│   ├── handlers/             # HTTP handlers
│   ├── middleware/           # HTTP middleware
│   ├── models/               # Data models
│   ├── repository/           # Data access layer
│   └── service/              # Business logic
├── scripts/                  # Utility scripts
│   ├── create-table.sh       # Create DynamoDB table
│   └── integration-test.sh   # Integration test script
└── docker-compose.yml        # Local development setup
```

### Running Tests

```bash
# Run integration tests
./scripts/integration-test.sh
```

## Production Considerations

1. **JWT Secret Key:** Use a secrets manager (AWS Secrets Manager, HashiCorp Vault)
2. **OTP Delivery:** Implement WhatsApp API integration
3. **Rate Limiting:** Add rate limiting middleware
4. **Monitoring:** Add Prometheus metrics and distributed tracing
5. **HTTPS:** Always use HTTPS in production
6. **Key Rotation:** Implement JWT secret key rotation strategy

## License

MIT
