# AI Gateway - Multi-Plane Management Makefile
# Targets:
#   run     - Run all three planes locally (Go, Python, Rust)
#   build   - Build binary artifacts for all planes
#   proto   - Re-generate gRPC code from proto/gateway.proto
#   clean   - Remove build artifacts and temporary files
#   docker  - Build and start using Docker Compose

.PHONY: run build proto clean docker

# Default target
run:
	@echo "--- Starting AI Gateway Plane components ---"
	@echo "[1/3] Starting Python Intelligence Plane..."
	cd logic-python && uv run main.py &
	@echo "[2/3] Starting Rust Nitro Plane..."
	cd utils-rust && cargo run --release &
	@echo "Waiting for backend services..."
	sleep 5
	@echo "[3/3] Starting Go Orchestration Plane..."
	cd core-go && go run ./cmd/gateway

build:
	@echo "--- Building AI Gateway artifacts ---"
	cd core-go && go build -o bin/gateway ./cmd/gateway
	cd utils-rust && cargo build --release
	# Python is interpreted, but we can ensure dependencies are synced
	cd logic-python && uv sync

proto:
	@echo "--- Regenerating gRPC stubs from proto/gateway.proto ---"
	# Go
	protoc --proto_path=proto --go_out=core-go --go-grpc_out=core-go proto/gateway.proto
	# Python
	cd logic-python && uv run python -m grpc_tools.protoc -I ../proto --python_out=. --grpc_python_out=. ../proto/gateway.proto
	# Rust (automatically handled by build.rs/tonic-build during next cargo build)

clean:
	@echo "--- Cleaning up artifacts ---"
	rm -rf core-go/bin/
	cd utils-rust && cargo clean
	rm -rf logic-python/__pycache__
	rm -rf logic-python/data/

docker:
	@echo "--- Launching via Docker Compose ---"
	docker-compose up --build

# Kubernetes / Minikube targets
k8s-build:
	@echo "--- Building images inside Minikube's Docker env ---"
	@echo "Make sure to run 'minikube docker-env' first!"
	docker build -t ai-gateway-orchestration:latest -f core-go/Dockerfile .
	docker build -t ai-gateway-intelligence:latest -f logic-python/Dockerfile .
	docker build -t ai-gateway-nitro:latest -f utils-rust/Dockerfile .

k8s-deploy:
	@echo "--- Deploying to Minikube ---"
	kubectl apply -f k8s/base.yaml
	kubectl apply -f k8s/nitro.yaml
	kubectl apply -f k8s/intelligence.yaml
	kubectl apply -f k8s/orchestration.yaml
	@echo "Waiting for pods..."
	kubectl get pods -n ai-gateway -w

k8s-clean:
	@echo "--- Deleting Kubernetes resources ---"
	kubectl delete -f k8s/orchestration.yaml
	kubectl delete -f k8s/intelligence.yaml
	kubectl delete -f k8s/nitro.yaml
	kubectl delete -f k8s/base.yaml
