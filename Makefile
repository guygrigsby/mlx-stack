.PHONY: build test test-go test-py install clean fakemlx install-launchd uninstall-launchd

GOFLAGS    ?=
PYTHON_BIN ?= $(HOME)/venvs/mlx/bin/python
INSTALL_DIR ?= $(HOME)/.local/bin

build:
	mkdir -p bin
	go build $(GOFLAGS) -o bin/mlxd    ./cmd/mlxd
	go build $(GOFLAGS) -o bin/mlxctl  ./cmd/mlxctl

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
	cp bin/mlxd bin/mlxctl $(INSTALL_DIR)/
	$(PYTHON_BIN) -m pip install -e ./python

clean:
	rm -rf bin

LAUNCHD_PLIST = $(HOME)/Library/LaunchAgents/dev.grigsby.mlxd.plist

install-launchd: install
	@mkdir -p $(HOME)/Library/LaunchAgents $(HOME)/.logs/mlx
	@sed -e "s|{{INSTALL_DIR}}|$(INSTALL_DIR)|g" -e "s|{{HOME}}|$(HOME)|g" \
		deploy/dev.grigsby.mlxd.plist.template \
		> $(LAUNCHD_PLIST)
	@echo "Installed launchd plist at $(LAUNCHD_PLIST)"
	@echo "Load:   launchctl load $(LAUNCHD_PLIST)"
	@echo "Unload: launchctl unload $(LAUNCHD_PLIST)"

uninstall-launchd:
	@if [ -f $(LAUNCHD_PLIST) ]; then \
		launchctl unload $(LAUNCHD_PLIST) 2>/dev/null || true; \
		rm $(LAUNCHD_PLIST); \
		echo "Removed $(LAUNCHD_PLIST)"; \
	else \
		echo "No plist at $(LAUNCHD_PLIST)"; \
	fi
