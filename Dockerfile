# Stage 1: Build the binary statically
FROM golang:1.27-alpine AS builder

WORKDIR /app

# Install git and ca-certificates
RUN apk add --no-cache git ca-certificates

# Cache dependencies
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# Build static binaries for server and burst runner
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /bin/server ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o /bin/burst ./cmd/burst

# Stage 2: Ultra-lightweight minimal runtime image
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata curl bash

# Run as non-root user for container security
RUN addgroup -S appgroup && adduser -S appuser -G appgroup
USER appuser

WORKDIR /app

COPY --from=builder /bin/server /app/server
COPY --from=builder /bin/burst /app/burst
COPY burst.sh /app/burst.sh

EXPOSE 8080

HEALTHCHECK --interval=5s --timeout=3s --retries=3 \
    CMD curl -f http://localhost:8080/ready || exit 1

ENTRYPOINT ["/app/server"]