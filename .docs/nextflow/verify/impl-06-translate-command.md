# Implementation: Command Building (translate.go, part 3)

**File:** `nextflowdsl/translate.go`

## Command Construction

### buildCommandWithValues (line 2537)
Wraps command body with:
1. Output capture (stdout/stderr to `.nf-stdout`/`.nf-stderr`)
2. Module load lines
3. Scratch directory handling
4. Before/after scripts
Returns final shell command string.

**Coverage:** DIR-beforeScript, DIR-afterScript, DIR-module, DIR-scratch,
PSEC-script, PSEC-shell, PSEC-exec

### buildCommandBody (line 2571)
Selects appropriate script renderer:
- `proc.Exec != ""` → renders exec section (evaluates as Groovy expression)
- `proc.Shell != ""` → renders shell section
- Otherwise → renders script section

**Coverage:** PSEC-script, PSEC-shell, PSEC-exec

### renderScript (line 2627)
Interpolates `$var` / `${expr}` in script template using input bindings + params.
**Coverage:** PSEC-script

### renderShellSection (line 2645)
Interpolates `!{expr}` in shell template. Preserves `$var` for bash.
**Coverage:** PSEC-shell

### interpolateKnownScriptVars (line 2860)
Interpolates known `$task.*`, `$params.*` and input variables in scripts.
Handles `task.ext.*` references via `taskExtInterpolationValue` (2909).
**Coverage:** DIR-dynamic (task.* in scripts), DIR-ext (task.ext.* in scripts)

### evalOutputCaptureLines (line 2937)
Generates shell commands to capture output declarations:
- `path` outputs → glob commands
- `env` outputs → echo $VAR
- `stdout` outputs → handled by capture wrapper
**Coverage:** OUT-path, OUT-env, OUT-stdout

### moduleLoadLines (line 2986)
Splits module directive (`mod1:mod2`) into `module load mod1` lines.
**Coverage:** DIR-module

### captureCommand (line 3006)
Wraps command in `{ ... } > stdout 2> stderr` capture.

## Scratch Handling

### resolveScratchDirective (line 3014)
Resolves scratch to: true (use tmpdir), string (specific path), or false.
**Coverage:** DIR-scratch

### wrapScratchCommand (line 3040)
Wraps command to:
1. Copy inputs to scratch dir
2. Run command in scratch
3. Copy outputs back
**Coverage:** DIR-scratch

## PublishDir Handling

PublishDir is handled in `translateProcessBindingSets` (line 1045) where
output paths are resolved and publish commands are appended.
The `PublishDir` struct (ast.go line 55) stores: Path, Pattern, Mode.

**Coverage:** DIR-publishDir, DIR-publishDir-path, DIR-publishDir-pattern,
DIR-publishDir-mode

## Output Path Resolution

### matchCompletedOutputPaths (line 361)
Matches completed job output paths against declared output patterns.
Handles glob patterns, `**` recursion, exact matches.

### resolveCompletedOutputValue (line 338)
Resolves output template against actual completed paths.
Templates with glob → matched paths; literals → direct.
