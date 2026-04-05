# Redis Configuration Guide

## Standard Redis Setup with Password Authentication

This application uses standard Redis with password-based authentication. No AWS IAM or serverless complexity required.

## Configuration

### Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `REDIS_ENDPOINT` | Yes | `localhost:6379` | Redis server endpoint |
| `REDIS_PASSWORD` | No | `` | Redis password (empty for no auth) |
| `REDIS_DB` | No | `0` | Redis database number (0-15) |
| `REDIS_USE_TLS` | No | `false` | Enable TLS encryption |

### Local Development

```bash
# Redis without password
export REDIS_ENDPOINT="localhost:6379"
export REDIS_PASSWORD=""
export REDIS_DB="0"
export REDIS_USE_TLS="false"
```

### Production Setup

```bash
# Redis with password authentication
export REDIS_ENDPOINT="your-redis-server.com:6379"
export REDIS_PASSWORD="your-secure-password"
export REDIS_DB="0"
export REDIS_USE_TLS="true"  # Recommended for production
```

### AWS ElastiCache (without IAM)

```bash
# ElastiCache with AUTH token
export REDIS_ENDPOINT="your-cache.abc123.cache.amazonaws.com:6379"
export REDIS_PASSWORD="your-auth-token"
export REDIS_DB="0"
export REDIS_USE_TLS="true"
```

## Redis Server Setup

### Using Docker (Development)

```bash
# Start Redis without password
docker run -d --name redis -p 6379:6379 redis:7-alpine

# Start Redis with password
docker run -d --name redis -p 6379:6379 \
  redis:7-alpine redis-server --requirepass yourpassword
```

### Using Docker Compose

Already configured in `docker-compose.yml`:

```bash
docker-compose up -d redis
```

### Installing Redis Locally

#### macOS
```bash
brew install redis
redis-server --requirepass yourpassword
```

#### Ubuntu/Debian
```bash
sudo apt-get install redis-server
sudo systemctl start redis-server
```

#### Configure Password
Edit `/etc/redis/redis.conf`:
```
requirepass your-secure-password
```

## Testing Connection

### Using redis-cli

```bash
# Without password
redis-cli -h localhost -p 6379 ping

# With password
redis-cli -h localhost -p 6379 -a yourpassword ping

# Expected response
PONG
```

### Using the Application

```bash
# Set environment variables
export JWT_SECRET_KEY="your-32-char-secret-key-here-12345"
export REDIS_ENDPOINT="localhost:6379"
export REDIS_PASSWORD="yourpassword"

# Run the server
./bin/qcom-server

# Check logs for successful connection
# You should see: "Redis client initialized successfully"
```

## Production Recommendations

### 1. Use Strong Password
```bash
# Generate a secure password
openssl rand -base64 32
```

### 2. Enable TLS
```bash
export REDIS_USE_TLS="true"
```

Configure Redis server for TLS:
```conf
# redis.conf
port 0
tls-port 6379
tls-cert-file /path/to/redis.crt
tls-key-file /path/to/redis.key
tls-ca-cert-file /path/to/ca.crt
```

### 3. Network Security
- Use private networks/VPCs
- Configure firewall rules
- Limit access by IP address
- Use security groups (AWS)

### 4. AWS ElastiCache Best Practices
- Enable encryption in transit
- Enable encryption at rest
- Use AUTH token (password)
- Enable automatic backups
- Use Multi-AZ for high availability

## Connection Pooling

The `go-redis` client automatically handles connection pooling. Default settings:
- Pool size: 10 connections per CPU
- Min idle connections: CPU count
- Connection timeout: 5 seconds
- Idle timeout: 5 minutes

## Troubleshooting

### Connection Refused
```bash
# Check if Redis is running
redis-cli ping

# Check firewall/security groups
telnet your-redis-host 6379
```

### Authentication Failed
```bash
# Verify password is correct
redis-cli -h localhost -p 6379 -a yourpassword ping

# Check REDIS_PASSWORD environment variable
echo $REDIS_PASSWORD
```

### TLS Errors
```bash
# Verify TLS is properly configured
openssl s_client -connect your-redis-host:6379 -starttls redis
```

## Example Configurations

### Development
```bash
export REDIS_ENDPOINT="localhost:6379"
export REDIS_PASSWORD=""
export REDIS_DB="0"
export REDIS_USE_TLS="false"
```

### Staging
```bash
export REDIS_ENDPOINT="staging-redis.example.com:6379"
export REDIS_PASSWORD="staging-password-123"
export REDIS_DB="0"
export REDIS_USE_TLS="true"
```

### Production
```bash
export REDIS_ENDPOINT="prod-redis.example.com:6379"
export REDIS_PASSWORD="$(aws secretsmanager get-secret-value --secret-id redis-password --query SecretString --output text)"
export REDIS_DB="0"
export REDIS_USE_TLS="true"
```

## Monitoring

### Key Metrics to Monitor
- Connection count
- Memory usage
- Hit rate
- Commands per second
- Latency

### Using redis-cli
```bash
# Monitor commands in real-time
redis-cli -a yourpassword monitor

# Get server info
redis-cli -a yourpassword info

# Check memory usage
redis-cli -a yourpassword info memory
```

## References

- [Redis Documentation](https://redis.io/documentation)
- [go-redis Documentation](https://redis.uptrace.dev/)
- [AWS ElastiCache for Redis](https://aws.amazon.com/elasticache/redis/)
- [Redis Security](https://redis.io/topics/security)

