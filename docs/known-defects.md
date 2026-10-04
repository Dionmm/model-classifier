# Known defects and limits

Verified items only. Read before changing nearby code; don't "fix" these
without updating this list.

| # | Area | What | Why it stays |
|---|---|---|---|
| 1 | Harness support | Copilot CLI and Claude Code teammates have no authoritative record of the model that ran, so they can't pass the transport check and stay disabled. | No signal found in hook payloads or the session store (`docs/design.md`, "Transport check"). |
| 2 | Harness output | Not yet confirmed that Claude applies `updatedInput`, or Copilot applies `modifiedArgs`, without a permission decision. | Only the live transport check can show this; until then `enabled` stays false. |
| 3 | Model names | The Copilot names `claude-haiku-4.5` and `claude-opus-5.5` come from the `task` tool's enum, not from logs. | Confirmed by the transport check, or corrected in config. |
| 4 | Warm-up | Whether Jev bills or rejects the `HEAD /` warm-up request is unknown. | If it does, change `Warm` to a TLS dial only. |
| 5 | Performance tests | The client start-up and client-plus-daemon tests log their medians but don't assert the targets. | Process-start timings are too noisy on shared CI runners to gate on. |
| 6 | Telemetry test | Setting `WithBlocking` on the batch span processor does not make the black-hole latency test fail with the current SDK (v1.46.0); the break-check used a synchronous exporter instead. | The test still proves exports to a dead collector don't add latency, but it doesn't specifically catch `WithBlocking`. |
| 7 | Jev | Jev isn't deterministic: confidence moves by up to 0.16 between runs of the same input, so decisions near a threshold can flip. | A property of the classifier; treat thresholds as about ±0.02 fuzzy. |
| 8 | Telemetry | Requests cancelled by the hook client are recorded with reason `jev_error` and `error_class` `canceled`. They do not count towards the circuit breaker, but they do appear under `jev_error` in the "decisions by reason" metric. | The design names no separate reason code; filter on `error_class` to tell them apart. |
| 9 | Policy test | `TestReleaseUploadGlobsFilesNotDirectories` checks that `shopt -s failglob` appears in `build-reusable.yml`, not that it comes before the `gh release upload` line. Moving it after the upload stays green. | Low risk: reviewers see the step as a whole. Tighten the test if that step changes again. |
