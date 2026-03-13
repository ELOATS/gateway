# Use a specialized uv-python image for fast dependency management
FROM ghcr.io/astral-sh/uv:python3.12-bookworm-slim

WORKDIR /app

# Enable bytecode compilation
ENV UV_COMPILE_BYTECODE=1

# Copy project files
COPY logic-python/pyproject.toml logic-python/uv.lock logic-python/requirements.txt ./logic-python/

# Install dependencies
RUN cd logic-python && uv sync --frozen --no-install-project

# Copy the rest of the application
COPY . .

# Expose the gRPC port
EXPOSE 50051

# Persistence directory
RUN mkdir -p /app/logic-python/data

ENV PYTHON_GRPC_PORT=50051
ENV GRPC_PORT=50051

# Set the working directory to where the app lives
WORKDIR /app/logic-python

# Single CMD to run the service
CMD ["uv", "run", "main.py"]
