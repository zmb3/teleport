.PHONY: assign
assign:
	go run cmd/bot.go assign-reviewers

.PHONY: check
check:
	go run cmd/bot.go check-reviewers

.PHONY: test
test:
	go test -race -v ./...

.PHONY: lint
lint:
	golangci-lint run
