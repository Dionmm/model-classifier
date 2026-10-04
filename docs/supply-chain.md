# Supply chain

Releases target **SLSA Build Level 3**: release builds run in the isolated
reusable workflow `.github/workflows/build-reusable.yml`, with callers limited
to declared inputs. Published binaries are built on hosted runners with
`-trimpath`, scanned with `govulncheck -mode=binary`, and published with
signed build provenance plus signed SPDX SBOM attestations through GitHub OIDC.

Consumers can verify a downloaded file with:

```sh
gh attestation verify ./model-router-darwin-arm64 \
  --repo Dionmm/model-classifier \
  --signer-workflow Dionmm/model-classifier/.github/workflows/build-reusable.yml \
  --source-ref refs/tags/vX.Y.Z
```

The expected signer identity is the `Dionmm/model-classifier` repository
running `.github/workflows/build-reusable.yml` for the exact `v*` release tag
named in `--source-ref`. The repository must protect `v*` tags with a ruleset,
because the release workflow grants `id-token: write` on tag pushes. That
ruleset is `protect-release-tags` on `refs/tags/v*`: it blocks creating,
updating, deleting and force-pushing those tags, and only repository admins
can bypass it.

Scanner exceptions live in `.github/scan-ignore.json` as `{id, reason,
expires}` entries. It is empty initially; CI checks only expiry/schema because
`govulncheck` findings are not currently filtered through that file.
