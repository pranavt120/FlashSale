.PHONY: help build run test load-test stop clean logs

help:
	@echo "FlashSale Backend - Phase-by-phase database learning"
	@echo ""
	@echo "Commands:"
	@echo "  make build        - Build Go binary"
	@echo "  make run          - Start docker-compose and run app"
	@echo "  make stop         - Stop all containers"
	@echo "  make clean        - Remove containers and volumes"
	@echo "  make load-test    - Run k6 load test"
	@echo "  make logs         - Tail app logs"

build:
	go build -o flashsale .

run: build
	docker-compose up -d
	sleep 3
	./flashsale

stop:
	docker-compose down

clean:
	docker-compose down -v
	rm -f flashsale

load-test:
	k6 run --vus 50 --duration 60s load_test_p0.js

logs:
	docker-compose logs -f
