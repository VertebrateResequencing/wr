# Phase 3: Module resolution

Ref: [spec.md](spec.md) sections C1, C2, C3

## Instructions

Use the `orchestrator` skill to complete this phase, coordinating
subagents with the `go-implementor` and `go-reviewer` skills.

## Items

### Batch 1 (parallel)

#### Item 3.1: C1 - Local module resolution [parallel with 3.2]

spec.md section: C1

Implement `NewLocalResolver(basePath string) ModuleResolver` in
`nextflowdsl/module.go`. Define the `ModuleResolver` interface.
Resolve `./` and `/` prefixed paths against the workflow file
directory. Return error for missing files. Covering all 3
acceptance tests from C1.

- [ ] implemented
- [ ] reviewed

#### Item 3.2: C2 - Remote module resolution (GitHub) [parallel with 3.1]

spec.md section: C2

Implement `NewGitHubResolver(cacheDir string) ModuleResolver` in
`nextflowdsl/module.go`. Cache modules under
`~/.wr/nextflow_modules/{owner}/{repo}/{revision}/`. Use
`git clone --depth 1` for fetching. Support cache hits to avoid
redundant network fetches. Tests use `t.TempDir()` as cache dir;
real-network tests should be guarded by an env var or mock.
Covering all 4 acceptance tests from C2.

- [ ] implemented
- [ ] reviewed

For parallel batch items, use separate subagents per item.
Launch review subagents using the `go-reviewer` skill
(review all items in the batch together in a single review
pass).

### Item 3.3: C3 - Chain resolver

spec.md section: C3

Implement `NewChainResolver(resolvers ...ModuleResolver)
ModuleResolver` in `nextflowdsl/module.go`. Try resolvers in
order, returning the first successful result. Covering all 2
acceptance tests from C3.

Depends on Items 3.1 and 3.2 for LocalResolver and
GitHubResolver.

- [ ] implemented
- [ ] reviewed
