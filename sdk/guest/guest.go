//go:build wasm

// Package guest provides the host-communication layer for WASM plugin authors.
//
// Import this package in your plugin's main(), then compile with tinygo:
//
//	tinygo build -o myplugin.wasm -target=wasi .
//
// Tool plugin example:
//
//	package main
//
//	import "github.com/yusheng-g/openagent-go/sdk/guest"
//
//	func main() {
//	    guest.Setup(guest.PluginOpts{
//	        Meta: guest.PluginMeta{
//	            Type:        "tool",
//	            Name:        "echo",
//	            Description: "Echoes input",
//	            Parameters:  `{"type":"object",...}`,
//	        },
//	        Handler: func(input []byte) ([]byte, error) {
//	            return []byte(`{"result":"ok"}`), nil
//	        },
//	    })
//	}
//
// Stage plugin example:
//
//	func main() {
//	    guest.Setup(guest.PluginOpts{
//	        Meta: guest.PluginMeta{
//	            Type:  "stage",
//	            Name:  "model_logger",
//	            Stage: "model.call",
//	            Phase: "enter",
//	        },
//	        Handler: func(input []byte) ([]byte, error) {
//	            return []byte(`{"action":"continue"}`), nil
//	        },
//	    })
//	}
package guest

import (
	"encoding/json"
	"unsafe"
)

// PluginMeta describes a plugin.
type PluginMeta struct {
	Type        string `json:"type"`                  // "tool" or "stage"
	Name        string `json:"name"`                  // unique name
	Description string `json:"description"`           // human-readable
	Parameters  string `json:"parameters,omitempty"`  // tool: JSON Schema (raw JSON)
	Stage       string `json:"stage,omitempty"`       // stage: stage name constant
	Phase       string `json:"phase,omitempty"`       // stage: "enter" | "leave" | "*"
}

// PluginOpts configures the plugin.
type PluginOpts struct {
	Meta    PluginMeta
	Handler func(input []byte) ([]byte, error)
}

var (
	exportMeta    []byte
	exportHandler func(input []byte) ([]byte, error)
	inputBuf      []byte // keep alive between alloc → execute; prevents GC
	outputBuf     []byte
)

// Setup must be called from init() (not main!) so it runs during _initialize
// in WASI reactor mode. For Go 1.24+ WASM, main() blocks via channel to keep
// the runtime alive after _start returns. For tinygo, main() is the entry
// point and also blocks.
func Setup(opts PluginOpts) {
	if exportHandler != nil {
		panic("guest.Setup called twice")
	}
	metaJSON, err := json.Marshal(opts.Meta)
	if err != nil {
		panic("marshal metadata: " + err.Error())
	}
	exportMeta = metaJSON
	exportHandler = opts.Handler
}

// ── WASM exports (called by host via ABI) ──

//go:wasmexport alloc
func alloc(size uint32) uint32 {
	inputBuf = make([]byte, size) // package-level ref prevents GC
	return uint32(uintptr(unsafe.Pointer(unsafe.SliceData(inputBuf))))
}

//go:wasmexport metadata
func metadata() uint64 {
	return packPtrLen(exportMeta)
}

//go:wasmexport execute
func execute(ptr uint32, length uint32) uint64 {
	return callHandler(ptr, length)
}

//go:wasmexport run
func run(ptr uint32, length uint32) uint64 {
	return callHandler(ptr, length)
}

func callHandler(ptr uint32, length uint32) uint64 {
	if exportHandler == nil {
		return packPtrLen([]byte(`{"error":"plugin not initialized"}`))
	}
	input := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(ptr))), int(length))
	output, err := exportHandler(input)
	if err != nil {
		return packPtrLen([]byte(`{"error":"` + err.Error() + `"}`))
	}
	return packPtrLen(output)
}

// packPtrLen packs (ptr, len) into a single uint64.
// Low 32 bits = length, high 32 bits = pointer offset.
func packPtrLen(data []byte) uint64 {
	if len(data) == 0 {
		return 0
	}
	ptr := uint64(uintptr(unsafe.Pointer(unsafe.SliceData(data))))
	return ptr<<32 | uint64(len(data))
}
