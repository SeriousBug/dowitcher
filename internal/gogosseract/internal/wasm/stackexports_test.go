package wasm

import (
	"context"
	"testing"

	"github.com/tetratelabs/wazero"
)

// TestAddStackExportAliases pins the whole reason this library is forked: if the
// aliases stop landing, OCR fails at the first indirect call rather than here.
func TestAddStackExportAliases(t *testing.T) {
	patched, err := tesseractWASMPatched()
	if err != nil {
		t.Fatalf("addStackExportAliases: %v", err)
	}

	ctx := context.Background()
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)

	mod, err := rt.CompileModule(ctx, patched)
	if err != nil {
		t.Fatalf("CompileModule: %v", err)
	}
	exports := mod.ExportedFunctions()
	for _, alias := range stackExportAliases {
		aliased, ok := exports[alias.to]
		if !ok {
			t.Fatalf("%s is not exported", alias.to)
		}
		original := exports[alias.from]
		if original == nil {
			t.Fatalf("%s is not exported", alias.from)
		}
		if len(aliased.ParamTypes()) != len(original.ParamTypes()) ||
			len(aliased.ResultTypes()) != len(original.ResultTypes()) {
			t.Errorf("%s does not have the signature of %s", alias.to, alias.from)
		}
	}
}

// Applying the patch to an already-patched module must be a no-op, so an
// upstream WASM rebuild that exports the new names needs no code change here.
func TestAddStackExportAliasesIsIdempotent(t *testing.T) {
	once, err := addStackExportAliases(tesseractWASM)
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	twice, err := addStackExportAliases(once)
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	if len(twice) != len(once) {
		t.Errorf("second pass changed the module: %d bytes, want %d", len(twice), len(once))
	}
}
