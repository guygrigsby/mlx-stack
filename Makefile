.PHONY: build test test-go test-py install clean fakemlx

GOFLAGS    ?=
PYTHON_BIN ?= $(HOME)/venvs/mlx/bin/python
INSTALL_DIR ?= $(HOME)/.local/bin

build:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/mlxd ./cmd/mlxd
	go build $(GOFLAGS) -o bin/mlx  ./cmd/mlx

fakemlx:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/fakemlx ./testdata/fakemlx

test: test-go test-py

test-go:
	go test ./...

test-py:
	$(PYTHON_BIN) -m pytest python/tests -v

install: build
	mkdir -p $(INSTALL_DIR)
	cp bin/mlxd bin/mlx $(INSTALL_DIR)/
	$(PYTHON_BIN) -m pip install -e ./python

clean:
	rm -rf bin
