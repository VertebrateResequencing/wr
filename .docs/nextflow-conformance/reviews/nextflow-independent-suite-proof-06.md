# Independent Suite Proofreading 06

Verdict: FIXED.

Reviewed only `.docs/nextflow-conformance/spec.md` as a written document.
Applied agent-conduct, spec-proofreader, prose-principles and unslop, and the
completion/liveness contract. No feature description, prompt, author report,
feature review, prior proof report or other requirement source was consulted.
Referenced local documents were checked for existence without reading them.

## Exact edit

In F3's Gradle IPC policy paragraph, four flags preceded "Neither flag proves
zero sockets." Replaced that sentence with "These flags do not prove zero
sockets." Rewrapped its first two lines within 80 columns. No behavioural
requirement changed. No other spec text changed.

Input spec SHA-256:
`53691311d01e970d6b6676ce8cb4989d121e1e2df7b6143689dcb00e0685649b`

Final spec SHA-256:
`57533dcf9d4948f9ac4ec8fd7984776deb95f360d43e10584797eb58376cee9c`

## Checks

- Read all 2,484 lines in bounded chunks.
- Checked repetition, contradictions, defined terms and prose quality.
  No remaining definite text error or reportable ambiguity was found.
- Confirmed sequential sections A-F and all 17 sequential story IDs under
  matching section headings.
- Confirmed all 69 unique acceptance IDs are sequential and inside their
  matching story blocks.
- Confirmed every story appears in Implementation Order. Its additional F11
  reference is explicitly defined as an external finding, not a story.
- Confirmed one h1, no skipped heading levels, named balanced code fences,
  80-column prose, ASCII outside fences, no trailing whitespace, no repeated
  blank lines and a final newline. Literal negative-test placeholder strings
  describe rejected inputs; there is no unfinished placeholder content.
- Confirmed all seven local link destinations exist. Reference-style document
  links resolve to definitions in this spec. Fixture reference examples are
  intentional quoted source, not document links.
- Verified the input hash before applying the one exact replacement and
  computed the final hash after the edit.

Only `spec.md` and this new report were written. No commits, pushes, nested
agents, installs, heavy workers, live commands or background work remain.
