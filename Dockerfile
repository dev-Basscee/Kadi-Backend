# Stage 1: Builder
FROM golang:1.23-alpine AS builder

# Install required dependencies for building and fetching SSL certificates
RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy dependency files first to cache them
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest of the source code
COPY . .

# Build the statically linked Go binary
# CGO_ENABLED=0 ensures it's fully static
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-w -s" -o kadi-backend ./cmd/server/main.go

# Stage 2: Production (Minimal)
FROM scratch

# Copy SSL certificates from the builder (required for external API/HTTPS calls)
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/

# Copy timezone data
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

# Copy the compiled binary
COPY --from=builder /app/kadi-backend /kadi-backend

# Expose the API port
EXPOSE 8080

# Command to run the application
ENTRYPOINT ["/kadi-backend"]
