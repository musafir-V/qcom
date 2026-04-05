# ============================================================
# Stage 1: Build
# ============================================================
FROM golang:1.23-alpine AS builder

RUN apk add --no-cache make gcc musl-dev

WORKDIR /app

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN make build

## ============================================================
## Stage 2: Runtime (minimal image)
## ============================================================
FROM alpine:3.19

RUN apk add --no-cache ca-certificates tzdata

RUN addgroup -S qcom && adduser -S qcom -G qcom

WORKDIR /app

COPY --from=builder /app/bin/qcom-server .

RUN chown -R qcom:qcom /app

USER qcom

ENV PORT=8080

EXPOSE 8080

HEALTHCHECK --interval=15s --timeout=5s --start-period=5s --retries=3 \
 CMD wget -qO- http://localhost:8080/health || exit 1

ENTRYPOINT ["./qcom-server"]