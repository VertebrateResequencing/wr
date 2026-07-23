# developers/

Developer tooling for reliability work on wr. **Not part of the shipped binary
or the test suite** — this directory contains only shell scripts, so
`go build ./...`, `make test`, and `make lint` never see it.

Start with [`../DEVELOPERS.md`](../DEVELOPERS.md), then use `wrdev.sh`:

```bash
developers/wrdev.sh help
```

`wrdev.sh` runs an **isolated** wr manager (its own config, ports, managerdir,
and `wrd_*` job names) so it can never disturb a real `--deployment production`
manager. It refuses to kill any process that is not its own isolated binary.
Everything it creates lives under `$WRDEV_ROOT` (default `$HOME/wr-devtest`).
