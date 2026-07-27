// Package gogosseract runs Tesseract OCR as a WebAssembly module under wazero.
//
// This is a vendored fork of github.com/danlock/gogosseract at commit 2521da5
// (2025-06-23), Apache 2.0, LICENSE kept alongside. Upstream's README says the
// library will not be updated and that dependents should pin wazero to the
// version in its own go.mod (v1.5.0), so there is nothing to send a patch to.
//
// It is forked because dowitcher is on wazero v1.12.0 — internal/cbz decodes
// AVIF through the same runtime — and gogosseract does not work past wazero
// v1.7.3. internal/wasm/stackexports.go carries the fix and the full
// explanation. Everything else is upstream, minus the parts dowitcher does not
// use: the worker pool, the tests, and the 9.6MB debug build of the WASM module
// that sat behind the gogosseract_debug build tag.
//
// gogosseract.New builds its own wazero.Runtime and keeps every moving part
// behind internal/, so the fix could not be applied from outside the module.
package gogosseract
