# customdb prototype

Throwaway code for `../README.md`. Its own module, so the root module's
`go list ./...`, `make lint` and `make test` ignore it (the directory also
starts with a dot). Build with the environment in `scripts/env.sh`, which
keeps the Go build and module caches off `$HOME`.

- `internal/flat`: zero-allocation codec for a Job spec and a 192-byte run
  state, with benchmarks against Binc.
- `internal/gc`: group-commit writer over one or more files; framed records
  with CRC32C; `Scan` stops at a torn tail.
- `internal/store`: the designs behind one interface (`wal` D2, `keyfiles`
  D1, `slots` D3, `sqlite` D4, `boltsmall`/`boltfull` baselines).
- `cmd/hotpath`: closed-loop runners plus adds; `-ship` (D5), `-lease` (D6),
  `-nosync`.
- `cmd/recoverbench`, `cmd/crashtest`, `cmd/fsyncbench`, `cmd/dbstats`.
- `internal/zc`: the spec and batch codecs (`-codec`, `-batchcodec`) and the
  corpus of real commands (`-corpus`) that `hotpath`, `recoverbench` and
  `crashtest` take; `internal/gc` compresses whole batches when given one.
- `cmd/compbench`: compressors on real records from a manager database;
  `-corpusout` and `-dictout` write the corpus and a zstd dictionary, and
  `cmd/bigcorpus` keeps the commands of at least 1KB.
- `cmd/nfsprobe`: hours of small and large appends with fdatasync, for how
  often a filesystem stalls.
- `hotpath -stallctl` holds every fsync through the soaks' `fusestall` mount
  for a timed stall; `-timeline` writes per-second counts.
- `scripts/`: the exact runs behind `../benchmarks.md`.
