# Makefile

BIN     := bin
SERVER  := $(BIN)/hitl-server
GATEWAY := $(BIN)/hitl-gateway

.PHONY: all build server gateway run-server run-gateway tidy clean

all: build

build: server gateway

server:
	@mkdir -p $(BIN)
	go build -o $(SERVER)  ./cmd/server
	@echo "Built → $(SERVER)"

gateway:
	@mkdir -p $(BIN)
	go build -o $(GATEWAY) ./gateway
	@echo "Built → $(GATEWAY)"

run-server: server
	EXPORT_DIR=../export \
	DATABASE_URL=postgres://postgres:postgres@localhost:5432/hitl \
	REDIS_HOST=localhost \
	PORT=8000 \
	./$(SERVER)

run-gateway: gateway
	BACKEND_URL=http://localhost:8000 \
	REDIS_HOST=localhost \
	GATEWAY_PORT=9000 \
	./$(GATEWAY)

tidy:
	go mod tidy

clean:
	rm -rf $(BIN)
