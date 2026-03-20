// Package wirehair provides a pure-Go Wirehair-compatible fountain code.
//
// The default build uses portable Go implementations only. On amd64 and arm64,
// the hot XOR kernels can be enabled with:
//
//	go build -tags wirehairsimd
//	go test -tags wirehairsimd ./...
package wirehair
