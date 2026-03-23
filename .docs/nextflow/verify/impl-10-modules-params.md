# Implementation: Module Loading & Params (module_load.go, module.go, params.go)

**File:** `nextflowdsl/module_load.go` (~650 lines)
**File:** `nextflowdsl/module.go` (~320 lines)
**File:** `nextflowdsl/params.go` (~270 lines)

## Module Loading

### LoadWorkflowFile (module_load.go line 39)
Public API — loads a `.nf` file, recursively resolving `include` imports.
**Coverage:** INCL-include

### ResolveWorkflowPath (line 54)
Resolves workflow path: local file, directory with `main.nf`, or remote spec.
**Coverage:** INCL-include (path resolution)

### loadWorkflowFile (line 198)
Internal recursive loader. Tracks loaded/loading to prevent cycles.
Calls `selectImportedDefinitions` for each import.

### selectImportedDefinitions (line 325)
Filters module workflow to only the named imports, applying aliases.
**Coverage:** INCL-include, INCL-alias, INCL-selective

### importedName (line 394)
Applies `include { FOO as BAR }` alias mapping.
**Coverage:** INCL-alias

### workflowDependencyTargets (line 519)
Determines process/subworkflow dependency graph for scheduling order.
**Coverage:** WF-call (dependency ordering)

## Module Resolvers

### NewGitHubResolver (module.go line 74)
Resolves `org/repo` specs by cloning from GitHub.
**Coverage:** INCL-remote (GitHub modules)

### NewLocalResolver (line 83)
Resolves local file paths relative to a base directory.
**Coverage:** INCL-include (relative paths)

### NewChainResolver (line 88)
Chains multiple resolvers, first success wins.

### parseGitHubModuleSpec (line 206)
Parses `owner/repo` or `owner/repo@revision` specs.

## Parameters

### LoadParams (params.go line 140)
Loads parameters from JSON or YAML file.
**Coverage:** PARAM-file (external params files)

### MergeParams (line 46)
Deep-merges multiple param maps (later sources override earlier).
**Coverage:** PARAM-merge (CLI params override config params)

### SubstituteParams (line 173)
Replaces `${params.name}` references in strings.
**Coverage:** PARAM-interp

### parseJSONParams (line 78) / parseYAMLParams (line 90)
Parse JSON/YAML param files, normalizing types.
**Coverage:** PARAM-json, PARAM-yaml

### resolveParamReference (line 256)
Resolves dotted param references: `params.input.path` → nested value.
**Coverage:** PARAM-nested
