# Implementation: Configuration (config.go)

**File:** `nextflowdsl/config.go` (~1370 lines)

## Data Structures

### skippedTopLevelConfigScopes (line 37)
Scopes that are parsed but ignored:
conda, dag, manifest, notification, report, timeline, tower, trace, wave, weblog.
**Coverage:** CFG-conda through CFG-weblog (all explicitly skipped)

### ProcessDefaults (line 51)
28-field struct holding all directive defaults:
Cpus, Memory, Time, Disk, Container, ErrorStrategy, MaxRetries, MaxForks,
PublishDir, Queue, ClusterOptions, Ext, ContainerOptions, Accelerator,
Arch, Shell, BeforeScript, AfterScript, Cache, Scratch, StoreDir, Module,
Conda, Spack, Fair, Tag, Directives, Env.
**Coverage:** DIR-* (all directives representable in config)

### ProcessSelector (line 86)
Holds: Kind (`withLabel`/`withName`), Pattern, Settings, Inner (nested selector).
**Coverage:** CFG-selectors

### Profile (line 93)
Holds: Process defaults, Selectors, Params, Executor settings.
**Coverage:** CFG-profiles

### Config (line 100)
Top-level: Params, Profiles, Process, Selectors, Executor, ContainerEngine, Env.
**Coverage:** CFG-params, CFG-process, CFG-env, CFG-executor, CFG-profiles

## Parsing

### ParseConfig / ParseConfigWithParams (line 110, 121)
Public API. Lexes input, then two-pass parsing:
1. `collectParams` → gather all param values first (needed for expressions)
2. `parse` → full parse with resolved params

### parse (line 170)
Main config parse loop. Handles top-level scopes:
- `params` → `parseParamsBlock` (308)
- `process` → `parseProcessBlock` (343)
- `profiles` → `parseProfilesBlock` (734)
- `env` → `parseTopLevelEnvBlock` (994)
- `executor` → `parseExecutorBlock` (1002)
- `docker`/`singularity`/`apptainer`/`podman`/`charliecloud`/`sarus`/`shifter` → `parseContainerScope` (1054)
- Skipped scopes → `skipNamedBlock` (909)
- Unknown scopes → `skipUnknownTopLevelConfigScope` (938)

**Coverage:** CFG-params, CFG-process, CFG-env, CFG-executor, CFG-profiles,
CFG-docker, CFG-singularity, CFG-apptainer, CFG-charliecloud..CFG-shifter

### parseProcessBlock (line 343) / parseProcessSettingsBlock (355)
Parses `process { ... }` with directive assignments and selectors.

### parseProcessAssignment (line 471)
Maps config key names to ProcessDefaults fields:
cpus, memory, time, disk, container, errorStrategy, maxRetries, maxForks,
publishDir, queue, clusterOptions, ext, containerOptions, accelerator,
arch, shell, beforeScript, afterScript, cache, scratch, storeDir, module,
conda, spack, fair, tag, env + arbitrary directives map.
**Coverage:** All DIR-* features via config

### parseProcessSelectorWithDepth (line 400)
Parses `withLabel:` / `withName:` selectors with nesting support.
**Coverage:** CFG-selectors

### parseProfilesBlock (line 734) / parseProfile (853)
Parses `profiles { name { ... } }` with process/params/executor overrides.
**Coverage:** CFG-profiles

### parseEnvBlock (line 958) / parseTopLevelEnvBlock (994)
Parses `env { KEY = 'value' }` blocks.
**Coverage:** CFG-env

### parseExecutorBlock (line 1002)
Parses `executor { name = 'slurm' ... }` settings.
**Coverage:** CFG-executor

### parseContainerScope (line 1054)
Parses `docker { enabled = true }` and similar.
Sets `ContainerEngine` when `enabled = true`.
**Coverage:** CFG-docker through CFG-shifter

### expandConfigIncludes (line 1323)
Handles `includeConfig 'path'` directives — loads and inlines tokens.
**Coverage:** CFG-includeConfig

### normalizeConfigPublishDirs (line 650)
Normalizes publishDir value (string, map, or list of maps) into `[]*PublishDir`.
**Coverage:** DIR-publishDir via config
