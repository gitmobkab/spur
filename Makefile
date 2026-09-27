include $(wildcard .env)
export

.PHONY: deps api worker cron test env

deps:   ; docker compose up -d
api:    ; go run ./cmd/api
worker: ; go run ./cmd/worker
cron:   ; go run ./cmd/cron
test:   ; go test ./...

# Create a local .env with fresh secrets.
env:
	@test ! -f .env || (echo ".env already exists" && exit 1)
	@sed -e "s/^ADMIN_TOKEN=$$/ADMIN_TOKEN=$$(openssl rand -hex 32)/" \
	     -e "s/^IP_HASH_SALT=$$/IP_HASH_SALT=$$(openssl rand -hex 32)/" .env.example > .env
	@echo "wrote .env"
