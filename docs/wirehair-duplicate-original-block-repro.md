# Reproducing the translated Wirehair duplicate-original-block panic

This document explains how to reproduce the panic that exists in the raw
translated [`fec/wirehair`](/home/shelikhoo/proj/src/github.com/xiaokangwang/fastTransfern/fec/wirehair) package when the decoder is fed the same original block more than once.

## Scope

This is a bug in the translated `fec/wirehair` decoder implementation itself.

It is **not** expected to reproduce through normal `transferd` use with
`FEC_ENGINE=wirehair`, because the adapter in
[`fec/wirehairfec/wirehair.go`](/home/shelikhoo/proj/src/github.com/xiaokangwang/fastTransfern/fec/wirehairfec/wirehair.go)
now deduplicates shard IDs before handing them to the translated decoder.

If you want to reproduce the underlying bug, you must test the raw
`fec/wirehair` package directly.

## Reproducer

A build-tagged reproducer test was added at:

- [`repro_duplicate_original_block_panic_test.go`](/home/shelikhoo/proj/src/github.com/xiaokangwang/fastTransfern/fec/wirehair/repro_duplicate_original_block_panic_test.go)

It is excluded from the normal test suite and only runs when you opt in with a
build tag.

## Command

Run:

```bash
env GOCACHE=/tmp/go-build-cache go test \
  -tags wirehairrepro \
  -run TestReproduceDuplicateOriginalBlockPanic \
  ./fec/wirehair
```

Expected result:

- the test should fail with a panic
- the panic should come from the translated decoder, typically in
  `ResumeSolveMatrix` or a nearby decode path

## What the reproducer does

The test:

1. creates a `wirehair.Encoder` for a 300-byte message using a 50-byte block size
2. creates a matching `wirehair.Decoder`
3. encodes original block `0`
4. feeds original block `0` to the decoder
5. feeds the same original block `0` again
6. continues feeding later blocks until the internal decoder state trips the panic

The important part is the duplicate of original block `0`. The second copy does
not cleanly reject as an application-level error. Instead, it appears to damage
internal decode state, and a later `Decode(...)` call panics.

## Why this reproducer uses a build tag

The reproducer is intentionally destructive:

- it is expected to panic
- it is expected to fail the test run

That is why it lives behind `-tags wirehairrepro` instead of running during
normal `go test ./...`.

## Manual API-level reproduction

If you prefer to reproduce it in a standalone program, the essential sequence is:

```go
payload := bytes.Repeat([]byte("abc"), 100)
enc, _ := wirehair.NewEncoder(payload, 50)
dec, _ := wirehair.NewDecoder(uint64(len(payload)), 50)

block0 := make([]byte, 50)
n, _ := enc.Encode(0, block0)
block0 = block0[:n]

dec.Decode(0, block0)
dec.Decode(0, block0) // duplicate original block

for id := uint32(1); id < 64; id++ {
    block := make([]byte, 50)
    n, _ := enc.Encode(id, block)
    dec.Decode(id, block[:n]) // eventually panics
}
```

## Notes

- If this command stops panicking in the future, the bug may have been fixed in
  the translated decoder.
- The runtime `wirehair` FEC option in `transferd` should still be safe against
  this specific issue because duplicate shard IDs are filtered in the adapter.
