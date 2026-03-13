# --- Build Stage ---
FROM golang:1.25-alpine AS builder

WORKDIR /app

# Copy the entire workspace root for .env access or context if needed, 
# but we specifically need core-go files.
# For simplicity, we assume we are building from the project root.
COPY core-go/go.mod core-go/go.sum ./core-go/
RUN cd core-go && go mod download

COPY . .

# Build the gateway binary
RUN cd core-go && go build -o /gateway ./cmd/gateway

# --- Run Stage ---
FROM alpine:latest

WORKDIR /

# Copy the binary from the builder
COPY --from=builder /gateway /gateway

# Expose the gateway port
EXPOSE 8080

# Environment defaults (can be overridden by docker-compose)
ENV PORT=8080
ENV PYTHON_ADDR=intelligence:50051
ENV RUST_ADDR=nitro:50052

CMD ["/gateway"]
