.PHONY: build test test-go test-py install clean fakemlx install-launchd uninstall-launchd dev

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

SHIM_DIR ?= $(HOME)/.local/share/mlx-stack/python

install: build
	mkdir -p $(INSTALL_DIR) $(SHIM_DIR)
	cp bin/mlxd bin/mlxctl $(INSTALL_DIR)/
	rsync -a --delete python/mlx_stack/ $(SHIM_DIR)/mlx_stack/
	@echo "Installed binaries to $(INSTALL_DIR), Python shim to $(SHIM_DIR)"

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

LAUNCHD_LABEL = dev.grigsby.mlxd

# Build + install binaries, then restart the launchd-managed mlxd so the new
# binary takes effect. Fast iteration target for framework changes.
dev: install
	@if launchctl list | awk '{print $$3}' | grep -qx "$(LAUNCHD_LABEL)"; then \
		launchctl kickstart -k gui/$$(id -u)/$(LAUNCHD_LABEL) && \
		echo "Kickstarted $(LAUNCHD_LABEL) — new binaries live"; \
	else \
		echo "$(LAUNCHD_LABEL) is not loaded in launchd."; \
		echo "Load it once with:"; \
		echo "    make install-launchd"; \
		echo "    launchctl load $(LAUNCHD_PLIST)"; \
		exit 1; \
	fi
