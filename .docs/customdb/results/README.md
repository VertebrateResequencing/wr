# Raw outputs

- `hp.out`: production-shape hot-path matrix (`proto/scripts/hp.sh`).
- `phase2.out`: saturation, crash tests, first recovery runs
  (`phase2.sh`). Its slots recovery failed with "negative offset": the
  prototype then wrote state slots without the spec offset. Fixed before
  `phase3.out`, which reran the slots crash tests and recovery. The script
  was stopped after the 800k D2 recovery.
- `phase3.out`: fixed-slots crash tests, D5 shipping, ceiling runs, the rest
  of recovery (`phase3.sh`, binaries built as `*2`). The 800k bbolt build was
  stopped after 38 minutes.
- `codec.bench`: `go test -bench . ./internal/flat`.
- `prod.stats`: `dbstats` on a copy of `runstate-gate/fixtures/prod.db`.
- fsyncbench output was not saved; its numbers are transcribed in
  `../benchmarks.md` section 1.
