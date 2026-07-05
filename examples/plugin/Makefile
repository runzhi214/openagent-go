.PHONY: all tool stage clean

PLUGINS_DIR := ./plugins
RUSTC := $(HOME)/.cargo/bin/rustc
RFLAGS := --target wasm32-unknown-unknown -C opt-level=z --edition 2021 --crate-type cdylib -C link-arg=--no-entry -C link-arg=--export=alloc -C link-arg=--export=metadata

all: tool stage

tool:
	$(RUSTC) $(RFLAGS) -C link-arg=--export=execute -o $(PLUGINS_DIR)/echo.wasm ./tool/echo.rs

stage:
	$(RUSTC) $(RFLAGS) -C link-arg=--export=run -o $(PLUGINS_DIR)/logger.wasm ./stage/logger.rs

clean:
	rm -f $(PLUGINS_DIR)/*.wasm
