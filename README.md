# model-classifier

Picks a model for each subagent spawn in Claude Code and Copilot CLI, using
[Jev](https://docs.typesafe.ai/introduction). The repo now has:

- `model-router`: the hook client (`hook`, `doctor`, `verify`).
- `model-routerd`: the long-lived daemon that owns config, the API key, Jev,
  decisions and telemetry.
- `router-spike`: the original payload-logging spike, kept for analysis.

## Build

```sh
go build -trimpath -o bin/model-router ./cmd/model-router
go build -trimpath -o bin/model-routerd ./cmd/model-routerd
go build -trimpath -ldflags="-s -w" -o bin/router-spike ./cmd/router-spike
go test -race ./...
```

## Install the daemon

Create a config and a strict API key file:

```sh
mkdir -p ~/.config/model-router ~/.model-router
cp examples/router/config.json ~/.config/model-router/config.json
printf '%s\n' "$TYPESAFE_API_KEY" > ~/.config/model-router/typesafe-api-key
chmod 0600 ~/.config/model-router/typesafe-api-key
```

Edit `api_key_file` in the config to the absolute key path. Both harnesses
start with `"enabled": false`; keep them disabled until `verify` proves the
transport applies changed models.

Install the launchd plist from `examples/launchd/com.dionmm.model-routerd.plist`,
replacing `/absolute/path/to/model-routerd` and `/Users/USERNAME` with absolute
paths, then load it:

```sh
launchctl bootstrap "gui/$(id -u)" ~/Library/LaunchAgents/com.dionmm.model-routerd.plist
launchctl kickstart -k "gui/$(id -u)/com.dionmm.model-routerd"
```

Run:

```sh
bin/model-router doctor
```

`doctor` checks that the client binary is absolute and executable, the socket
exists, daemon `/v1/health` matches the protocol, the config is readable, and
the daemon sees an API key. It also prints hook snippets with the absolute
client path.

## Install hooks

Use absolute paths. Examples are in `examples/router/`.

Claude Code:

```json
{
  "hooks": {
    "PreToolUse": [
      {
        "matcher": "Agent|Task",
        "hooks": [
          {
            "type": "command",
            "command": "/ABSOLUTE/PATH/TO/model-router hook --harness claude",
            "timeout": 3
          }
        ]
      }
    ]
  }
}
```

Copilot CLI:

```json
{
  "version": 1,
  "hooks": {
    "preToolUse": [
      {
        "type": "command",
        "matcher": "task",
        "bash": "/ABSOLUTE/PATH/TO/model-router hook --harness copilot",
        "timeoutSec": 3
      }
    ]
  }
}
```

Set `MODEL_ROUTER_MODE=off` to make the hook exit before reading stdin or
touching any file.

## Transport check

Before enabling a harness, register one-time nonces and put each nonce in the
spawn prompt:

```sh
bin/model-router verify --harness claude --action force:deep
bin/model-router verify --harness claude --action none
```

For Claude subagents, correlate by `PostToolUse` `tool_response.resolvedModel`.
Run a contrasting pair, a no-change control, and absent-model checks before
setting `harnesses.<name>.enabled` or adding no-model `agent_types` in the
daemon config.

## Reviewing decisions

The daemon writes decision lines to `~/.model-router/decisions.jsonl` with
tiers, scores, reasons, ids and sizes, never prompt text. Review overrides after
enabling; disable a bad direction by setting its threshold above `1.0`, or
remove a bad fill type from `no_model.agent_types`.

## Telemetry

OpenTelemetry is configured by the standard `OTEL_*` environment variables in
the daemon environment, for example `OTEL_EXPORTER_OTLP_ENDPOINT`. Export is
best-effort and non-blocking. Client-side daemon failures are recorded separately
in `~/.model-router/client-errors.jsonl` without payload text.

## Payload-logging spike

`router-spike` still changes nothing: it logs spawn payloads and exits 0 with
no output, so the harness continues as if no hook ran. Logs go to
`~/.model-router-spike/` (override with `MODEL_ROUTER_SPIKE_DIR` or `--dir`).
Prompts can contain code and secrets, so review logs before sharing them.

```sh
bin/router-spike stats
TYPESAFE_API_KEY=... bin/router-spike bench --limit 50
TYPESAFE_API_KEY=... bin/router-spike bench --modes reuse --limit 0 \
  --questions examples/questions-v4.json --out bench-v4.jsonl
```

The spike answers payload-shape, prompt-size, Jev-latency and agreement
questions. It does not prove which hook output field a harness honours; use the
`model-router verify` transport check for that.
