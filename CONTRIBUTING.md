#Contributing to SoroProbe

Thanks for your interest. SoroProbe is built to be extended: the core is small
and the two things people most often want to add — a new ScVal type and a new
RPC method — are deliberately one-file changes.

## Getting set up

```bash
git clone https://github.com/soroworks/soroprobe
cd soroprobe
make all      # tidy, fmt, vet, test, build
```

You need Go, but **not** necessarily Go 1.25. The Stellar SDK requires it, and
`GOTOOLCHAIN=auto` (Go's default since 1.21) will fetch the right toolchain
automatically. If you have Go 1.21 or newer installed, `make` just works.

Useful targets:

| Target | Does |
| --- | --- |
| `make test` | Run the suite |
| `make test-race` | Run the suite under the race detector |
| `make lint` | Run golangci-lint (pinned to the version CI uses) |
| `make cover` | Report total coverage |
| `make fixtures` | Re-record test fixtures from the live testnet |
| `make help` | List everything |

## Ground rules

**Tests must never touch the network.** `go test ./...` has to pass on a machine
with no internet. The suite replays responses recorded from the public testnet
into `internal/stellar/stellartest/testdata`. If you need new network data, add
it to the recorder rather than reaching out from a test.

**SoroProbe is read-only.** It simulates and inspects. Pull requests that submit
transactions, deploy contracts, or perform restorations are out of scope — that
boundary is what makes the tool safe to point at mainnet and safe to run in CI.
Reporting that a restore is *required*, with its fee, is in scope; performing
one is not.

**SoroProbe never handles secret keys.** It builds unsigned transactions from a
public key. Nothing should ever accept, store, log or require a secret key.

**SoroProbe is stateless.** No database. It queries the chain live. If a feature
seems to need persistence, please open an issue to discuss it before building.

## Project layout

```
cmd/soroprobe     cobra CLI and output rendering
internal/config   defaults, config file and environment resolution
internal/stellar  RPC client behind an interface, ledger keys, transaction building
  └── stellartest fixture-backed fake, plus the recorder that captures fixtures
internal/scval    ScVal encode/decode behind an interface, with a type registry
internal/health   TTL interpretation: thresholds, statuses, durability semantics
internal/probe    simulate / inspect / check orchestration
internal/api      chi HTTP handlers mirroring the CLI
```

The CLI and the API both render the same result structs from `probe`. Adding a
field there surfaces it in both; please do not add a field to only one.

## Common contributions

### Adding an ScVal type

Encoders live in a registry keyed by type name.

1. Write an `EncodeFunc` in `internal/scval/encode.go`:

   ```go
   func encodeMyType(literal string) (xdr.ScVal, error) { ... }
   ```

2. Register it in `registerBuiltins`:

   ```go
   r.Register("mytype", encodeMyType)
   ```

3. Add the matching case to `Decode` in `internal/scval/decode.go`. The
   `default` branch there returns an error naming `scval.Decode`, which is your
   cue that a type is encodable but not decodable.

4. Add a row to the table-driven test in `internal/scval/scval_test.go`. The
   `TestEncodeExplicitTypes` table asserts a full encode/decode round trip, so
   one row covers both directions.

5. Add the type to the `argHelp` table in `cmd/soroprobe/commands.go` and to the
   README's argument table.

Nothing outside `internal/scval` needs to change.

### Adding an RPC method

1. Add the method to the `Client` interface in `internal/stellar/client.go`.
2. Implement it on `RPCClient` in the same file.
3. Implement it on `Fake` in `internal/stellar/stellartest/stellartest.go` —
   the compile-time `var _ stellar.Client = (*Fake)(nil)` assertion will tell
   you if you forget.
4. If it needs fixture data, add it to the recorder and run `make fixtures`.

### Adding or recording a fixture

When adding a feature or test case that needs new RPC responses (for example, a
contract invocation requiring arguments or auth, or a contract with specific data
keys), record an individual fixture without modifying the default set:

```bash
# Record a simulated contract function call with arguments:
go run ./internal/stellar/stellartest/record \
  --contract <contract-id> \
  --fn <function-name> \
  --arg u32:5 \
  --out internal/stellar/stellartest/testdata/simulate_my_call.json

# Record ledger entries (instance and data keys):
go run ./internal/stellar/stellartest/record \
  --contract <contract-id> \
  --key sym:Admin \
  --out internal/stellar/stellartest/testdata/ledger_entries_my_contract.json
```

Arguments and data keys reuse SoroProbe's standard `type:value` argument syntax.
Fixtures are pretty-printed and stable so diffs remain clean.

### Changing health thresholds or statuses

`internal/health` is self-contained and has no dependency on RPC types, which
makes it the easiest package to work in. Statuses are ordered by `Severity()`;
if you add one, place it in that ordering and extend the ordering test.

### Re-verifying after a protocol upgrade

Soroban's state model changes between protocol versions. After an upgrade:

```bash
make fixtures       # re-record against the live testnet
make test           # the suite now runs against the new data
```

Then update the version table at the top of the README, including the protocol
version and the date verified. If field names moved, the compile will tell you.

## Style

- Idiomatic Go. `make fmt`, `make vet` and `make lint` must be clean.
- Small, focused packages with a clear job.
- Comments explain **why**, not what. If a decision looks arbitrary, say what
  the alternative was and why it lost.
- Errors say what failed and quote the offending input.
- Interface things that need to be swappable or faked; do not interface things
  that do not.

## Pull requests

- One logical change per PR.
- Include tests. Bug fixes should include a test that fails without the fix.
- Update the README when you change user-visible behavior.
- CI runs formatting, `go mod tidy` verification, vet, build and `go test -race`.

## Reporting bugs

Please include the SoroProbe version (`soroprobe version`), the network and RPC
endpoint, the exact command, and the output — `--json` output is especially
helpful. If it involves a specific contract, the contract ID lets us reproduce
it directly.

## License

By contributing you agree that your contributions are licensed under Apache-2.0,
the same license as the project.
