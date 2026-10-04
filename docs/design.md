# model-router design (architecture B)

Status: v2, implemented (30 Sep 2026); review rounds complete (see
"Implementation status"). No harness is enabled yet; each waits for its
transport check. Replaces the single-process draft (architecture A).
Evidence: `router-spike` logs (234 records, both harnesses) and `bench` runs
v1-v4 (117 distinct spawns, `jev-1.13.0`). Anything marked **unverified**
blocks enforcement in the affected harness until checked.

## Implementation status (4 Oct 2026)

### Done

- All packages in "Package layout" are built: `model-router` (`hook`,
  `doctor`, `verify`), `model-routerd`, and the launchd plist, hook configs
  and daemon config under `examples/`. `router-spike` still works on the
  moved packages.
- Every request-flow step (client 1-7, daemon 1-12 and 4a) is implemented in
  the order given, with the reason codes listed.
- Supply chain: CI, a reusable build workflow (SLSA L3 provenance and SBOM
  attestations), release, zizmor, Renovate, a scan-ignore expiry check and
  `docs/supply-chain.md`. `govulncheck` is a `go.mod` tool.
- `docs/known-defects.md` lists the verified limits.
- Review: three code-review rounds on frozen snapshots. Round 1 found 4
  blocking and 12 non-blocking issues, round 2 found 0 blocking and 4
  non-blocking, round 3 found 0 blocking and 4 non-blocking. All were fixed,
  each with a break-check.
- Last local run, after the round-3 fixes: `gofmt -l .` empty, `go vet ./...`
  clean, `go test -race -count=2 ./...` passing.
- `govulncheck` was reported clean by the round-3 fixer. I didn't re-run it
  myself after those fixes.

### Left to do

1. ~~**Review the round-3 fixes.**~~ Done (4 Oct 2026). Rounds 4-6 reviewed
   them on pinned snapshots and fixed what they found, each with a
   break-check:
   - The stalled-body test was made deterministic, and it now fails instead
     of hanging.
   - Client-cancelled Jev calls no longer count towards the breaker (known
     defect 8).
   - Decision-log drop counts are exact except for a partial write inside
     `Flush`.
   - A partial last line is ended with a newline before the log appends.
   - Both `timeout_ms` bounds are tested.

   The final round was clear: gofmt, vet, `go test -race -count=3` and
   source-mode govulncheck all pass locally.
2. ~~**Put the repo under version control.**~~ Done (4 Oct 2026): public at
   `Dionmm/model-classifier`. CI ran on GitHub for the first time, and its
   binary-mode govulncheck caught GO-2026-6443 in grpc, which the
   source-mode scan had reported as not called. PR #1 fixed it. The release
   workflow first runs with the first tag. zizmor runs only on PRs that touch
   `.github/` and has not run on GitHub yet.
3. ~~**Repo settings.**~~ Done (4 Oct 2026). The ruleset
   `protect-release-tags` covers `refs/tags/v*`: it blocks creating,
   updating, deleting and force-pushing those tags, and only repository
   admins can bypass it.
4. ~~**Cut a first release**~~ Done (4 Oct 2026): `v0.1.1`. `v0.1.0` published
   no binaries because of a bug in the publish step (fixed in PR #3), and its
   release notes mark it as broken. `v0.1.1` has 8 binaries and 8 SPDX SBOMs.
   `gh attestation verify` passes for provenance and SBOM with
   `--source-ref refs/tags/v0.1.1`, and it fails for the wrong tag or a
   tampered file.
5. **Install locally.** Fill in the plist placeholders, write the config and
   a 0600 API key file, add the hook configs, then run `model-router doctor`.
6. **Transport check per harness**, Claude Code subagents first (see
   "Transport check"). Only then set `enabled` and add fill `agent_types`.
   Copilot and Claude teammates stay unsupported until an authoritative
   resolved-model signal is found.
7. **Review live decisions** from `decisions.jsonl` and tune per direction,
   for example `max->deep` to 0.80 (see "Reviewing decisions").
8. **Resolve the unverified items** in `docs/known-defects.md`: Copilot model
   names, whether `HEAD /` warm-up is billed, and whether the harness applies
   the change with no permission decision.

The break-check records (`breakchecks-p1..p3`, `breakchecks-r1..r3`) and the
review snapshots are in the agent session folder, not this repo. Copy them
in if they should be kept.

## Goal

Before every subagent spawn in Claude Code and Copilot CLI, ask Jev which tier
the task needs. When Jev is confident and disagrees with the orchestrator,
replace the spawn's top-level `model`. Otherwise change nothing.

Non-goals: effort levels, non-spawn tools, trimming the prompt the subagent
receives.

## Decisions taken

| Question | Answer |
|---|---|
| Architecture | B: long-lived daemon plus a thin hook client |
| Copilot transport | Command hook client, not an HTTP hook (see below) |
| Spawns with no `model` | Fill when confident, only for agent types verified in the transport check |
| Shadow phase | None. A harness goes to enforce once the transport check shows it really runs the swapped model |
| Thresholds | Per direction, all 0.90 to start; `max->deep` is the candidate for 0.80 later |
| Questions | `examples/questions-v4.json`, embedded; `jev-1.13.0` pinned |

## Components

```
harness ─stdin─► model-router hook  ──HTTP over unix socket──►  model-routerd
         ◄stdout─ (thin client,                                 (launchd agent:
                  always exit 0)                                 warm Jev conn,
                                                                 decision logic,
                                                                 OTel SDK)
```

- **`model-router hook`**: short-lived client, one per hook invocation. Reads
  stdin, forwards it to the daemon, writes back whatever the daemon returns
  (possibly nothing), exits 0. Holds no config, no API key, no OTel SDK.
- **`model-routerd`**: runs under a launchd user agent (`RunAtLoad`,
  `KeepAlive`). Owns config, the API key, the Jev HTTP client (kept warm), the
  decision rule and all telemetry.
- One Go module, two binaries built from `cmd/model-router` and
  `cmd/model-routerd`.

### Why a command client for Copilot

Copilot hooks reference (docs.github.com, fetched 30 Sep 2026):

- HTTP `preToolUse` hooks fail open, but the `url` field "must use `https://`"
  for `preToolUse`. The `COPILOT_HOOK_ALLOW_LOCALHOST=1` exception for
  `http://localhost` conflicts with that line; which wins is **unverified**.
  HTTPS on localhost would need a locally trusted cert inside Copilot's Node
  runtime.
- Command `preToolUse` hooks fail closed on a crash or non-zero exit, but
  **timeouts always fail open**.

A client that exits 0 on every path it controls is therefore fail-open except
for failures outside the process: binary missing or not executable, a fatal Go
runtime error (not recoverable by `recover`), or `SIGKILL`. `model-router
doctor` checks the first; the client is small enough that the others are
unlikely.

Claude Code uses the same client. Its hooks reference (code.claude.com,
fetched 30 Sep 2026) says a `PreToolUse` command hook that exits non-zero
other than 2, or times out, does not block the tool call; only exit 2 blocks.
Claude also supports HTTP hooks (connection failure is non-blocking), but one
client for both harnesses keeps the paths identical.

## Invariants

1. **Fail open.** Every error, timeout, panic, unknown payload, daemon
   unavailability or version mismatch gives exit 0 and empty stdout. The
   client has one exit path behind a top-level `recover`.
2. **Never grant permission.** Output never contains `permissionDecision`.
   If a harness only applies a modified model together with `allow`, that
   harness stays unsupported (the router emits nothing for it).
3. **Only the top-level `model` changes.** The daemon edits the raw JSON
   object: decode args as `map[string]json.RawMessage`, replace or insert the
   `model` key, re-encode. Every other value's bytes pass through unchanged
   (large integers, nested unknown fields, `null`).
4. **Prompt text goes only to Jev.** Telemetry carries sizes, tiers, scores
   and ids, never prompt or description text.
5. **Performance is measured, not assumed.** Targets below are benchmarks in
   CI, not guarantees.

## Hook configuration

- Copilot, native `preToolUse` with `"matcher": "task"` so no other tool runs
  the hook, `timeoutSec: 3`.
- Claude Code, `PreToolUse` with `"matcher": "Agent|Task"`, `timeout: 3`.
- The client still checks the tool name itself (matchers are config and can be
  wrong) and exits silently for anything else.

## Request flow

### Client (`model-router hook`)

1. If `MODEL_ROUTER_MODE=off`, exit 0 without reading anything else. This is
   the "off does no I/O" switch.
2. Read stdin up to 4 MiB. Over the cap: exit 0, no output.
3. Stream-parse only until the tool name is found (`tool_name` or `toolName`).
   Not a spawn tool (`Agent`, `Task`, `task`): exit 0.
4. `POST /v1/route` to `~/.model-router/run/router.sock` with the raw payload
   and headers `X-Router-Protocol: 1`, `X-Router-Harness` (from a `--harness`
   flag set in the hook config) and `X-Router-Deadline` (the client's
   absolute deadline, process start + 1500 ms, as Unix milliseconds; client
   and daemon share the host clock).
5. On 200, write the body to stdout verbatim. On anything else, write nothing.
6. After stdout is written, on a client-side failure (connect refused,
   deadline, bad status) and only if the deadline has not passed, append one
   line to `~/.model-router/client-errors.jsonl`: opened `O_APPEND`,
   non-blocking `flock` (`LOCK_NB`; contended means drop), skipped if the file
   is over 1 MiB. This is the only way the router learns the daemon was down.
   It is best effort, not a count.
7. Exit 0.

The client's deadline (1.5 s) is under the hook timeout (3 s). The harness
timeout only fires if the client itself hangs, and in both harnesses a
timeout does not block the spawn.

### Daemon (`model-routerd`)

1. Check the peer uid on the socket (`getpeereid`) matches the daemon's. The
   socket directory is 0700.
2. Protocol header mismatch: 200 with empty body, telemetry `version_skew`.
3. Admission: at most 32 requests in flight. Over that, return empty
   immediately, telemetry `overloaded`.
4. Parse the payload (reuse `spike.Parse` shapes). The shape found in the
   payload must match `X-Router-Harness`; otherwise emit nothing, telemetry
   `harness_mismatch`. Extract spawn args; for Copilot, `toolArgs` may be a
   JSON string and is decoded once.
4a. Verification nonce: if the prompt contains an active nonce registered for
    this harness (see "Transport check"), consume it and skip steps 5-11 and Jev.
    `force:<tier>` returns that tier's output model, bypassing `enabled` and
    `agent_types`; `none` returns empty. Reason `verify`. Nothing else can
    bypass those checks.
5. Read the model field (tri-state), using the harness's **exact** input alias
   map (not substring matching):
   - **absent**: a fill candidate only if `subagent_type` is in the harness's
     `no_model.agent_types`; otherwise emit nothing, telemetry
     `no_model_skipped`;
   - **string in the alias map**: normal policy;
   - **empty, non-string or not in the map**: emit nothing, telemetry
     `unmapped_model`.
6. Build state `{subagent_type, description, prompt}`, serialise it, and
   check the byte budget (see "Context limit"). Over budget: emit nothing,
   telemetry `over_budget`.
7. Circuit breaker open (see below): emit nothing, telemetry `circuit_open`.
8. Call Jev with a context deadline of `min(X-Router-Deadline - 100 ms,
   now + config timeout)`. The 100 ms reserves time to build and return the
   output. Already past: skip, telemetry `deadline`. No retry.
9. Validate the response (see below). Invalid: emit nothing, telemetry
   `bad_response`.
10. Decide. No override: 200 with empty body.
11. Override, but harness not `enabled` in config: 200 with empty body; the
    decision line records the would-be override with reason `disabled`.
12. Override: build the harness output, check the new model name is in that
    harness's output map, return it.

### Circuit breaker

A single timeout budget still adds up to 1.4 s to every spawn while Jev is
down. After 3 consecutive transport errors, timeouts or 5xx, the breaker
opens for 30 s and every spawn skips Jev. After 30 s, one request probes; on
success the breaker closes, on failure it reopens. State changes are logged.

### Connection warm-up

The Go HTTP client connects lazily and the server may drop idle connections,
so "daemon is up" does not mean "connection is warm". At start, and when the
connection has been idle for 60 s, the daemon dials TLS to the Jev host with a
`HEAD /` (cost and behaviour of that request **unverified**; if it is billed
or rejected, warm-up is only the TLS dial). First-spawn and post-idle
latency are benchmarked separately from warm latency.

## Validating Jev's response

Act only when all hold, otherwise treat as an error:

- HTTP 200, body at most 64 KiB (the spike allows 4 MiB; real bodies are
  under 1 KiB), and it decodes.
- `model` equals the pinned version.
- `answers.tier.type == "choice"` and `choice` is one of `Tiers`.
- `confidence` is finite and in [0, 1].
- `probabilities` has exactly the keys in `Tiers`, each finite and in [0, 1],
  summing to 1 within 0.02 (v4 values are rounded to 2 places).
- The chosen tier has the highest probability (ties allowed).

## Tiers and model names

| Tier | Claude Code | Copilot CLI |
|---|---|---|
| fast | `haiku` | `claude-haiku-4.5` |
| balanced | `sonnet` | `claude-sonnet-5` |
| deep | `opus` | `claude-opus-5.5` |
| max | `fable` | `claude-fable-5.1` |

Copilot names match the `model` enum of Copilot CLI 1.0.89's `task` tool;
`claude-sonnet-5`, `claude-fable-5` and `claude-fable-5.1` also appear in the
logs.

Each harness has two maps in config:

- **input aliases**: exact strings to tier, e.g. Copilot `claude-fable-5` and
  `claude-fable-5.1` both to `max`. Anything not listed is `unmapped_model`
  and never overridden. `spike.ModelTier`'s substring rule stays in the
  spike's reports only.
- **output**: tier to the one model name emitted.

## Decision rule

Policy `override_confident`:

```json
{
  "thresholds": {"default": 0.90},
  "harnesses": {
    "copilot": {"no_model": {"agent_types": [], "threshold": 0.90}}
  }
}
```

- Keys are `from->to` tier pairs or `*->to`. Lookup order: exact pair, then
  `*->to`, then `default`. A value above 1.0 switches that direction off.
- Fills have their own per-harness block. A fill happens when `subagent_type`
  is in `agent_types` and Jev's confidence meets the fill threshold and the
  `*->to` threshold for its tier. `agent_types` starts empty; types are added
  only after the transport check covers them. An empty list is the "leave"
  setting.
- Every direction starts at 0.90. `max->deep` (Fable to Opus) is the
  candidate for 0.80 once live decision logs support it; at a flat 0.80 the
  v4 replay had 2 wrong overrides of 13.
- v4 replay at 0.90: 9 overrides, all judged correct (7 Fable to Opus,
  1 Opus to Fable, 1 Sonnet to Opus). With every agent type allowed, there
  would also be 2 fills to `fast` at 0.97 for Copilot `research`/explore
  spawns ("Survey e2e streaming infra", "Codebase facts for Apple live
  apps"); the transport check must first show what the harness picks for
  those agents by default.
- Jev's confidence measures how clear-cut the answer is, not the chance of
  matching the orchestrator (agreement was 74% in the 0.70-0.90 band).
- Jev is not deterministic. Re-running the same 117 v4 inputs on the same
  version (`jev-1.13.0`) moved 27 confidences by more than 0.02 (max 0.16)
  and changed 1 choice (a low-confidence one). Around 0.90 this flips
  decisions: the second run made 10 overrides instead of 9 ("Review #554-556
  plan", Fable to Opus, 0.89 then 0.90). Treat the threshold as fuzzy by
  about ±0.02 and don't tune it from a single run.

## Context limit

Jev allows 32k tokens for state plus the longest question. The router never
trims: trimming would change the task Jev sees, and head/tail trimming would
hide exactly the middle of long prompts.

- Budget from the worst observed ratio, not the median: the serialised state
  (the JSON bytes sent, after escaping) must fit
  `(32000 - 726 - 1000 margin) x 2.6 = 78,700` bytes. 726 is the whole v4
  baseline request (all questions, empty state), a conservative stand-in for
  "the longest question", which was not measured separately. 2.6 is under the
  observed minimum of 2.62 chars/token. Bytes are used because they are at
  least the character count.
- Over budget: no override, telemetry `over_budget` with the size. The
  largest real prompt so far is 10.5k characters (13% of the budget).
- If `over_budget` appears in practice, design chunking then. Note that
  "highest tier across chunks" is biased towards `max`.
- There is no harness-side trimming hook; it would cut what the subagent
  receives.

## Output per harness

| Harness | Output on override |
|---|---|
| Claude Code `PreToolUse` | `{"hookSpecificOutput":{"hookEventName":"PreToolUse","updatedInput":{...}}}` |
| Copilot native `preToolUse` | `{"modifiedArgs":{...}}` |

Exactly one JSON object, no trailing output. For Copilot, `modifiedArgs` is
the decoded args object even when the input `toolArgs` was a string.

## Enabling a harness

A harness is enabled in config (`harnesses.<name>.enabled`) once the
transport check below passes. There is no separate sign-off test on the
classifier; its decisions are reviewed from the decision log after
enabling (see "Reviewing decisions"). Until enabled, requests that get as
far as a decision are classified and logged, and would-be overrides are
recorded with reason `disabled`; nothing is emitted. Requests stopped earlier
(overload, unmapped model, over budget, open breaker) log that reason
instead.

### Transport check (live)

Forcing is never global. `model-router verify --harness <h> --action <a>`
registers a one-time nonce with the daemon over the socket and prints it.
`<a>` is `force:<tier>` (emit that tier's model, whether the spawn had a
model or not) or `none` (route normally but emit nothing, as a control). The
daemon applies the action only to a spawn from that harness whose prompt
contains the nonce, within 10 minutes, once. Every other request is routed
normally. The nonce is a random test marker, not user content, so logging it
does not break invariant 4.

1. A forced contrasting pair (spawn asks for `haiku`, router forces `deep`;
   and the reverse), a control spawn with a "no change" nonce, and an
   absent-model spawn run once unforced (to record the harness default for
   that agent type) and once with a forced fill.
2. Correlate by session id and tool-use id with an authoritative record of the
   model that ran. The echoed argument and the subagent's own report do not
   count.
3. Fills are enabled per agent type, only for types checked in step 1: an
   absent model may mean an agent-specific default, not "no preference".

| Harness | Authoritative signal | Status |
|---|---|---|
| Claude Code subagent | `PostToolUse` `tool_response.resolvedModel` (16 records in the logs) | available |
| Claude Code teammate | none found | unsupported |
| Copilot CLI | none found in hook payloads or the session store | unsupported |

Also to confirm through the transport check: Claude's docs describe `updatedInput` as
replacing the input and pairing with `allow` or `ask`, but don't state
outright that it applies with no decision; Copilot's docs list `modifiedArgs`
as a separate field, same gap. Hook processes don't need the API key in B;
only the daemon does.

## Telemetry

The spawn never waits on telemetry export or file I/O. Span creation and
metric recording still run in the request goroutine; their overhead is
benchmarked. All telemetry except the client-error line lives in the daemon.

- **OpenTelemetry SDK, on by default.** One span per request, a child span
  for the Jev call, and metrics: decisions by reason, Jev latency histogram,
  in-flight gauge. OTLP/HTTP exporter configured by the standard `OTEL_*`
  environment variables (default `http://localhost:4318`).
- Non-blocking: the SDK's batch span processor drops spans when its bounded
  queue is full unless `WithBlocking` is set; it is never set. Periodic metric
  reader. If no collector is listening, exports fail in the background; an
  OTel error handler counts export failures and a counter tracks dropped
  decision-log lines.
- **Decision log** for tuning: one JSONL line per spawn to
  `~/.model-router/decisions.jsonl` (0600), written by a single goroutine
  from a buffered channel. Full channel means drop. Rotate at 10 MiB, keep 3.
  Contents: harness, session and tool-use ids, orchestrator model and tier,
  Jev tier, confidence, probabilities, Jev version, decision, reason,
  state bytes, input tokens, Jev latency, error class. No prompt text.
- Per path: `MODEL_ROUTER_MODE=off` does no I/O; non-spawn tools do no I/O
  after stdin; every spawn reaching the daemon makes exactly one non-blocking
  attempt to record a span and a decision line (drops are counted);
  client-side failures make one best-effort client-error line.

## Performance targets

Benchmarked, not promised:

- Client, non-spawn payload (including a 4 MiB one): under 10 ms process
  start to exit.
- Client plus daemon, spawn, excluding Jev: under 5 ms.
- Measured locally (darwin/arm64, release build flags, median of repeated
  runs, 30 Sep 2026): client non-spawn 3.3 ms, non-spawn 4 MiB 7.0 ms,
  client plus daemon spawn 3.5 ms. The hook client avoids `net/http` (it
  speaks minimal HTTP/1.1 over the socket) to keep start-up cost down. The
  tests log these numbers rather than assert them, so CI isn't flaky.
- Jev call, v4 questions, 117 spawns, measured 30 Sep 2026:
  - warm connection: p50 230 ms, p90 275 ms, p99 319 ms;
  - new connection: p50 262 ms, p90 298 ms, p99 408 ms, max 548 ms.
    Connection setup (DNS, TCP, TLS) is p50 36 ms, so warm-up saves about
    30 ms. The slowest calls had the largest prompts (about 5k characters).
  This is the first-spawn and post-idle cost; the 1500 ms client deadline
  leaves ample margin.
- Jev black-holed: with the breaker closed, one spawn pays the full budget;
  after 3 failures, spawns pay under 5 ms.

## Config

`~/.config/model-router/config.json`, read by the daemon at start. Change
means `launchctl kickstart -k`. Fields: Jev endpoint, pinned model, timeout,
thresholds, per-harness `enabled`, alias and output maps and `no_model`
block, questions path
(default embedded v4). API key from a 0600 key file named in config; launchd
agents don't inherit the shell environment, so `TYPESAFE_API_KEY` is only a
fallback.

## Package layout

- `cmd/model-router`: client (`hook`, `doctor`, `verify`), logic in
  `internal/client`.
- `cmd/model-routerd`: daemon, logic in `internal/daemon`.
- `internal/wire`: the client-daemon contract (paths, headers, verify and
  health types, default file locations).
- `internal/payload`: moved from `spike` (parse, spawn args, raw model edit).
- `internal/jev`: client, response types, validation, moved from `spike`.
- `internal/router`: budget, decision rule, output building.
- `internal/telemetry`: OTel setup, decision log writer.
- `internal/repopolicy`: test-only repository policy checks (workflows,
  Renovate, client dependencies).
- `internal/spike` and `router-spike` keep working on the moved packages.

## Reviewing decisions

After a harness is enabled, review its overrides from
`~/.model-router/decisions.jsonl` (a report like `router-spike`'s agreement
report). A direction that makes bad swaps is switched off by setting its
threshold above 1.0; a fill type by removing it from `agent_types`. Threshold
changes, such as `max->deep` to 0.80, are made from this log. Note that the
v4 wording and thresholds were tuned on the same 117 spawns they were judged
on, so live results may be worse than the v4 replay.

## Supply chain

Release binaries are built in CI with signed SLSA provenance and an SBOM,
scanned before publish. Dependencies (the OTel SDK is the main one) are pinned
by `go.sum`; Actions by digest; updates by Renovate.

## Test plan

Each test carries a named break-check.

- Decision table: every from/to pair and a `*->to` key, at, just below and just
  above each threshold; absent, empty, non-string and unmapped model
  (including a name that contains a tier word but is not in the alias map).
- Harness mismatch, disabled harness, fill for an agent type not allowed.
- Deadline: `X-Router-Deadline` honoured; the daemon returns before the
  client deadline with Jev hanging; an already-past deadline skips Jev.
- Circuit breaker: opens after 3 failures, probes once, closes on success.
- Verify nonce: each action applies only to the matching spawn and harness,
  once, before expiry; `force:<tier>` emits on a disabled harness and on an
  absent-model spawn of a type not in `agent_types`; without a nonce, neither
  emits.
- Raw edit: only `model` changes; large ints, nested unknown fields, `null`,
  string-encoded `toolArgs`.
- Response validation: each rule rejects on its own (e.g. `choice=deep`,
  `probabilities={"fast":1}`).
- Budget: exactly at, one byte over, measured on serialised bytes with
  quotes, backslashes, control and multi-byte characters.
- Client fail-open: daemon absent, slow (past deadline), 500, bad protocol,
  panic injected; each gives exit 0 and empty stdout.
- Client fast path: non-spawn payload makes no socket connection and writes
  no file (temp home asserted untouched); `MODEL_ROUTER_MODE=off` reads no
  stdin.
- Daemon: overload returns empty; uid checked on every accepted connection
  before HTTP handling; telemetry exporter pointing at a black-hole endpoint
  does not change request latency; Jev at a black-hole endpoint trips the
  breaker.
- Output: one JSON object per harness shape; no `permissionDecision` ever.
