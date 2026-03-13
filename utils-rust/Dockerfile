# --- Build Stage ---
# 使用支持 Rust 2024 Edition 的版本 (1.85+)
FROM rust:1.85-slim AS builder

RUN apt-get update && apt-get install -y protobuf-compiler && rm -rf /var/lib/apt/lists/*

WORKDIR /app

# Copy the workspace context for proto files
COPY . .

# Build the Rust service
RUN cd utils-rust && cargo build --release

# --- Run Stage ---
FROM debian:bookworm-slim

WORKDIR /

# Copy the binary from the builder
COPY --from=builder /app/utils-rust/target/release/utils-rust /utils-rust

# Expose the gRPC port
EXPOSE 50052

ENV RUST_GRPC_PORT=50052
ENV GRPC_PORT=50052

CMD ["/utils-rust"]
