# FEC benchmark notes

This document records local benchmark results for the currently integrated FEC
engines:

- `raptorq`
- `wirehair`
- `wirehair` built with `wirehairsimd`

These results are not protocol benchmarks. They measure the codec work only.

## Test setup

Machine used:

- OS: Linux
- Arch: amd64
- CPU: Intel Core i7-10875H @ 2.30GHz

Shared benchmark settings:

- shard size: `1300` bytes
- source symbol counts tested: `1024`, `2048`, `4096`
- payload size = `source_symbols * 1300`

Benchmark harness:

- [`encode_bench_test.go`](/home/shelikhoo/proj/src/github.com/xiaokangwang/fastTransfern/fec/fecbench/encode_bench_test.go)

Commands used:

Plain build:

```bash
env GOCACHE=/tmp/go-build-cache go test -tags fecbench -run=^$ -bench . -benchmem ./fec/fecbench
env GOCACHE=/tmp/go-build-cache go test -tags fecbench -run=^$ -bench BenchmarkDecodeRecover -benchmem ./fec/fecbench
```

SIMD build:

```bash
env GOCACHE=/tmp/go-build-cache go test -tags 'fecbench wirehairsimd' -run=^$ -bench . -benchmem ./fec/fecbench
env GOCACHE=/tmp/go-build-cache go test -tags 'fecbench wirehairsimd' -run=^$ -bench BenchmarkDecodeRecover -benchmem ./fec/fecbench
```

## Benchmarks

### Encoder initialization

This measures encoder construction only: `GetEncoder3(...)`.

| Source symbols | Payload | Wirehair | Wirehair SIMD | RaptorQ |
| --- | ---: | ---: | ---: | ---: |
| 1024 | 1.33 MB | 13.45 ms, 98.95 MB/s | 1.94 ms, 684.96 MB/s | 11.90 ms, 111.87 MB/s |
| 2048 | 2.66 MB | 24.75 ms, 107.59 MB/s | 3.69 ms, 722.03 MB/s | 39.28 ms, 67.79 MB/s |
| 4096 | 5.32 MB | 51.93 ms, 102.54 MB/s | 8.20 ms, 649.71 MB/s | 218.63 ms, 24.36 MB/s |

Observations:

- Without SIMD, Wirehair and RaptorQ are close at `1024` symbols.
- Without SIMD, Wirehair is clearly faster at `2048` and `4096` symbols.
- With `wirehairsimd`, Wirehair encoder setup is much faster than RaptorQ at all tested sizes.

### Shard generation

This measures repeated `GetShard(...)` calls after the encoder is already built.

| Source symbols | Wirehair | Wirehair SIMD | RaptorQ |
| --- | ---: | ---: | ---: |
| 1024 | 4.22 us/shard, 308 MB/s | 0.518 us/shard, 2511.75 MB/s | 0.647 us/shard, 2010.37 MB/s |
| 2048 | 3.96 us/shard, 329 MB/s | 0.523 us/shard, 2486.62 MB/s | 0.654 us/shard, 1988.27 MB/s |
| 4096 | 4.29 us/shard, 303 MB/s | 0.588 us/shard, 2212.24 MB/s | 0.679 us/shard, 1914.52 MB/s |

Observations:

- Plain Wirehair is much slower than RaptorQ for steady-state shard generation.
- `wirehairsimd` changes that completely on this machine and makes Wirehair faster than RaptorQ at all tested sizes.

### Full decode and recover

This measures:

1. create decoder
2. feed encoded shards until the decoder reports success
3. recover the original payload

The benchmark uses about `1.33x` as many shards as source symbols, generated in advance from the matching encoder.

| Source symbols | Payload | Wirehair | Wirehair SIMD | RaptorQ |
| --- | ---: | ---: | ---: | ---: |
| 1024 | 1.33 MB | 1.424 ms, 934.46 MB/s | 1.414 ms, 941.61 MB/s | 14.09 ms, 94.49 MB/s |
| 2048 | 2.66 MB | 3.071 ms, 866.98 MB/s | 3.115 ms, 854.76 MB/s | 47.90 ms, 55.58 MB/s |
| 4096 | 5.32 MB | 5.359 ms, 993.66 MB/s | 5.528 ms, 963.24 MB/s | 242.07 ms, 22.00 MB/s |

Observed allocation counts:

| Source symbols | Wirehair | Wirehair SIMD | RaptorQ |
| --- | ---: | ---: | ---: |
| 1024 | 35 allocs/op | 35 allocs/op | 2131 allocs/op |
| 2048 | 44 allocs/op | 44 allocs/op | 4193 allocs/op |
| 4096 | 61 allocs/op | 61 allocs/op | 8283 allocs/op |

Observations:

- For full decode and recover, Wirehair is much faster than RaptorQ across all tested sizes on this machine.
- `wirehairsimd` did not materially change the end-to-end decode benchmark here.

## Practical takeaways

- If you build without SIMD, Wirehair still looks attractive for larger part sizes because encoder setup and full decode are both strong.
- If you build with `wirehairsimd` on supported hardware, Wirehair is the strongest performer in these local benchmarks for both encode and decode.
- RaptorQ remains functional and stable, but in these measurements it paid a large setup and decode cost as the symbol count increased.

## Caveats

- These are single-machine codec benchmarks, not network transfer benchmarks.
- Results may differ substantially across CPUs, especially for `wirehairsimd`.
- The benchmark uses a fixed shard size of `1300` bytes to match the current transfer default.
- Different loss patterns and different numbers of repair symbols can change the practical decode cost.
