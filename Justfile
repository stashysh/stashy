default:
    @just --list

# Run the server
run:
    go run ./cmd/stashy serve --migrate

# Build the binary
build:
    go build -o stashy ./cmd/stashy

# Generate proto code
generate:
    buf generate

# Run database migrations
migrate:
    go run ./cmd/stashy migrate

# Tidy dependencies
tidy:
    go mod tidy

# Lint proto files
lint:
    buf lint

# Check for known vulnerabilities in code paths we call
vuln:
    go run golang.org/x/vuln/cmd/govulncheck@latest ./...

# Clean build artifacts
clean:
    rm -f stashy
    rm -f *.db *.db-journal *.db-wal *.db-shm
