.PHONY: up down logs rescan mixes smoke player test test-python test-go test-flutter build

COMPOSE ?= docker compose
BASE ?= http://127.0.0.1:8787

# Load .env if present (for TOKEN/PASSWORD in make targets)
ifneq (,$(wildcard .env))
include .env
export
endif

up:
	$(COMPOSE) up -d --build

down:
	$(COMPOSE) down

logs:
	$(COMPOSE) logs -f --tail=200

player:
	go -C player build -o bin/musik-player ./cmd/musik-player

test-go:
	go -C player test ./...

test-python:
	python -m pytest

test-flutter:
	cd mobile/flutter && flutter test

test: test-python test-go test-flutter

build: player

rescan:
	@test -n "$(MUSIK_API_TOKEN)" || (echo "set MUSIK_API_TOKEN" >&2; exit 1)
	curl -sS -X POST -H "Authorization: Bearer $(MUSIK_API_TOKEN)" \
	  -H "Content-Type: application/json" -d '{}' \
	  $(BASE)/api/library/rescan

mixes:
	@test -n "$(MUSIK_API_TOKEN)" || (echo "set MUSIK_API_TOKEN" >&2; exit 1)
	curl -sS -X POST -H "Authorization: Bearer $(MUSIK_API_TOKEN)" \
	  -H "Content-Type: application/json" -d '{}' \
	  $(BASE)/api/jobs/mix_pack

smoke:
	@chmod +x scripts/smoke_api.sh
	MUSIK_BASE=$(BASE) MUSIK_PASSWORD="$(MUSIK_PASSWORD)" MUSIK_API_TOKEN="$(MUSIK_API_TOKEN)" \
	  ./scripts/smoke_api.sh

bench:
	@chmod +x scripts/bench_queue.sh
	./scripts/bench_queue.sh
