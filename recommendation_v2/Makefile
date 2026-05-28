MIGRATE = migrate -path migrations -database "$(DATABASE_URL)" -verbose

# Default DSN for local dev (overridable via env)
DATABASE_URL ?= postgres://admin:Sea-TryGo@localhost:35432/first_db?sslmode=disable

.PHONY: migrate-up migrate-down migrate-create migrate-version

migrate-up:   ## Run all pending migrations
	$(MIGRATE) up

migrate-down: ## Rollback the last migration
	$(MIGRATE) down 1

migrate-create: ## Create new migration (usage: make migrate-create NAME=add_something)
	$(MIGRATE) create -ext sql -seq $(NAME)

migrate-version: ## Show current migration version
	$(MIGRATE) version
