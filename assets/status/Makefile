.PHONY: run-pr
run-pr: build
	./build/status pr

.PHONY: run-milestone
run-milestone: build
	./build/status milestone

.PHONY: build
build:
	mkdir -p build/
	go build -o build/status cmd/status.go

.PHONY: clean
clean:
	rm -fr build/
