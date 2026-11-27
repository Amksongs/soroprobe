## Summary

- Adds SBOM generation (SPDX-JSON via Syft) and build provenance attestation generation to the release workflow, so each archive ships an SBOM and each artifact is attestable with `gh attestation verify`.
- Adds a coverage gate CI workflow that posts a coverage report to the job summary and fails when total coverage drops by more than 1 percentage point compared with main. The figure excludes the fixture recorder and test helpers.
- Adds a fixture-drift workflow that re-records fixtures weekly and runs the test suite against the fresh fixtures, so protocol changes are detected automatically.
- Adds a `gen-man` helper and `make man` target that generate man pages from the CLI help text, and bundle the generated `docs/man/soroprobe.1` into release archives.

This is the CI hardening track: reproducibility metadata, a coverage floor, fixture integrity, and offline documentation artifacts.

## Validation

### Pre-checks intended to pass
- `make lint`
- `make test`
- `make cover`
- `make man`
- `make build`

Touched files:
- `.github/workflows/coverage.yml`
- `.github/workflows/fixtures.yml`
- `.github/workflows/release.yml`
- `.goreleaser.yaml`
- `Makefile`
- `cmd/soroprobe/gen-man/main.go`

### Checked behaviors
- The release pipeline now emits an SBOM and attestation outputs alongside the archives without breaking the existing build steps.
- The coverage gate compares against main on pull requests and fails only when the drop exceeds the documented threshold.
- The fixture-drift workflow re-records once per week and then runs the suite against the fresh fixtures.
- Man pages are generated from the CLI help text and included in the release archive manifest.

## Manual QA

- Open the latest `release` workflow run on a `v*` tag and confirm the SBOM and attestation artifacts are attached.
- Open a PR from this branch and confirm `coverage.yml` posts the coverage summary and compares against main.
- Confirm `fixtures.yml` can run via `workflow_dispatch` and that `make fixtures` plus `make test` still pass locally.
- Run `make man` locally and inspect `docs/man/soroprobe.1` for completeness against the current CLI.

Closes #40
Closes #41
Closes #42
Closes #45
