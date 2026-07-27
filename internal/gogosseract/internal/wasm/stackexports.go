package wasm

import (
	"encoding/binary"
	"fmt"
	"sync"
)

// wazero's emscripten invoke_* trampolines call two module exports around every
// indirect call, to save and restore the C stack so a longjmp can unwind it.
// Up to wazero v1.7.3 those were "stackSave" and "stackRestore"; v1.8.0 switched
// to "emscripten_stack_get_current" and "_emscripten_stack_restore", the names a
// newer emscripten emits. tesseract-core.wasm was built by an emscripten old
// enough to export only the first pair, so on wazero >= v1.8.0 the very first
// indirect call panics with "emscripten_stack_get_current not exported".
//
// The two pairs are the same functions under different names: get_current
// returns the stack pointer, restore takes one back. So rather than reimplement
// the trampolines (they need wazero's internal packages for the funcref table
// lookup, which is not reachable from outside that module), add two more entries
// to the WASM export section pointing at the functions that are already there.
//
// Roughly the JS emscripten emits for an indirect call, for reference:
//
//	function invoke_iii(index,a1,a2) {
//	  var sp = stackSave();
//	  try { return getWasmTableEntry(index)(a1,a2); }
//	  catch(e) { stackRestore(sp); if (e !== e+0) throw e; _setThrew(1, 0); }
//	}
var stackExportAliases = []struct{ from, to string }{
	{"stackSave", "emscripten_stack_get_current"},
	{"stackRestore", "_emscripten_stack_restore"},
}

// tesseractWASMPatched is the embedded module with those aliases added. Done
// once and kept, because the result is what every runtime compiles.
var tesseractWASMPatched = sync.OnceValues(func() ([]byte, error) {
	return addStackExportAliases(tesseractWASM)
})

const wasmExportSectionID = 7

func addStackExportAliases(mod []byte) ([]byte, error) {
	start, end, err := findSection(mod, wasmExportSectionID)
	if err != nil {
		return nil, err
	}

	payload := mod[start:end]
	count, n, err := readU32(payload)
	if err != nil {
		return nil, fmt.Errorf("export section count: %w", err)
	}

	funcIndexes := map[string]uint32{}
	off := n
	for range count {
		name, kind, index, next, err := readExport(payload, off)
		if err != nil {
			return nil, err
		}
		off = next
		if kind == 0 {
			funcIndexes[name] = index
		}
	}
	if off != len(payload) {
		return nil, fmt.Errorf("export section has %d trailing bytes", len(payload)-off)
	}

	added := make([]byte, 0, 64)
	extra := uint32(0)
	for _, alias := range stackExportAliases {
		if _, ok := funcIndexes[alias.to]; ok {
			continue
		}
		index, ok := funcIndexes[alias.from]
		if !ok {
			return nil, fmt.Errorf("tesseract-core.wasm exports neither %s nor %s", alias.from, alias.to)
		}
		added = binary.AppendUvarint(added, uint64(len(alias.to)))
		added = append(added, alias.to...)
		added = append(added, 0) // export kind: func
		added = binary.AppendUvarint(added, uint64(index))
		extra++
	}
	if extra == 0 {
		return mod, nil
	}

	newPayload := binary.AppendUvarint(nil, uint64(count+extra))
	newPayload = append(newPayload, payload[n:]...)
	newPayload = append(newPayload, added...)

	// The section header is the id byte plus the LEB128 length, and the length
	// grew, so the header is rebuilt rather than patched in place.
	out := make([]byte, 0, len(mod)+len(added)+8)
	out = append(out, mod[:sectionHeaderStart(mod, start)]...)
	out = append(out, wasmExportSectionID)
	out = binary.AppendUvarint(out, uint64(len(newPayload)))
	out = append(out, newPayload...)
	out = append(out, mod[end:]...)
	return out, nil
}

// sectionHeaderStart walks back from a section payload to its id byte. The
// length between them is however many bytes the LEB128 size took.
func sectionHeaderStart(mod []byte, payloadStart int) int {
	i := payloadStart - 1
	for i > 0 && mod[i-1]&0x80 != 0 {
		i--
	}
	return i - 1
}

// findSection returns the payload bounds of the first section with the given id.
func findSection(mod []byte, id byte) (start, end int, err error) {
	if len(mod) < 8 || string(mod[:4]) != "\x00asm" {
		return 0, 0, fmt.Errorf("not a WASM module")
	}
	off := 8
	for off < len(mod) {
		sectionID := mod[off]
		off++
		size, n, err := readU32(mod[off:])
		if err != nil {
			return 0, 0, fmt.Errorf("section %d size: %w", sectionID, err)
		}
		off += n
		if off+int(size) > len(mod) {
			return 0, 0, fmt.Errorf("section %d runs past end of module", sectionID)
		}
		if sectionID == id {
			return off, off + int(size), nil
		}
		off += int(size)
	}
	return 0, 0, fmt.Errorf("no section with id %d", id)
}

func readExport(payload []byte, off int) (name string, kind byte, index uint32, next int, err error) {
	nameLen, n, err := readU32(payload[off:])
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("export name length: %w", err)
	}
	off += n
	if off+int(nameLen)+1 > len(payload) {
		return "", 0, 0, 0, fmt.Errorf("export entry runs past end of section")
	}
	name = string(payload[off : off+int(nameLen)])
	off += int(nameLen)
	kind = payload[off]
	off++
	index, n, err = readU32(payload[off:])
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("export %q index: %w", name, err)
	}
	return name, kind, index, off + n, nil
}

func readU32(b []byte) (uint32, int, error) {
	v, n := binary.Uvarint(b)
	if n <= 0 || v > 1<<32-1 {
		return 0, 0, fmt.Errorf("bad LEB128")
	}
	return uint32(v), n, nil
}
