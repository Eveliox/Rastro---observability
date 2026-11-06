# Shortcuts so you don't have to remember long curl commands.
# Run `make <target>`, e.g. `make order`.

ITEM ?= sku-keyboard

.PHONY: up down logs ps order break-inventory flaky-inventory slow-inventory heal-inventory fault-status

up:               ## build images and start everything in the background
	docker compose up --build -d

down:             ## stop and remove the containers
	docker compose down

logs:             ## follow logs from all services
	docker compose logs -f

ps:               ## show running containers
	docker compose ps

order:            ## place one order (ITEM=sku-mouse make order)
	@curl -s -i -X POST localhost:8081/checkout -H 'Content-Type: application/json' \
		-d '{"item_id":"$(ITEM)","quantity":1}'; echo

break-inventory:  ## inventory fails every request
	@curl -s -X PUT localhost:8083/admin/fault -d '{"error_rate":1.0}'; echo

flaky-inventory:  ## inventory fails 30% of requests
	@curl -s -X PUT localhost:8083/admin/fault -d '{"error_rate":0.3}'; echo

slow-inventory:   ## inventory adds 800ms to every request
	@curl -s -X PUT localhost:8083/admin/fault -d '{"latency_ms":800}'; echo

heal-inventory:   ## clear inventory's fault
	@curl -s -X DELETE localhost:8083/admin/fault; echo

fault-status:     ## show the current fault on every service
	@for p in 8081 8082 8083; do printf "$$p: "; curl -s localhost:$$p/admin/fault; done
