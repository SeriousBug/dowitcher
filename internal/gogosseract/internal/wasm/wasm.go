package wasm

import (
	_ "embed"
)

//go:embed tesseract-core.wasm
var tesseractWASM []byte
