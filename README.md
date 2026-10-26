# SoroProbe

[![CI](https://github.com/soroworks/soroprobe/actions/workflows/ci.yml/badge.svg)](https://github.com/soroworks/soroprobe/actions/workflows/ci.yml)
[![Security](https://github.com/soroworks/soroprobe/actions/workflows/security.yml/badge.svg)](https://github.com/soroworks/soroprobe/actions/workflows/security.yml)
[![Release](https://img.shields.io/github/v/release/soroworks/soroprobe)](https://github.com/soroworks/soroprobe/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/soroworks/soroprobe)](go.mod)
[![License](https://img.shields.io/github/license/soroworks/soroprobe)](LICENSE)

A health and simulation checker for Stellar/Soroban smart contracts.

Before you invoke a Soroban contract for real, you want to know three things:
will this call succeed, what will it cost, and is the contract's state actually
healthy — or is it about to expire and start failing? Today that means ad-hoc
CLI simulation and manual reasoning about TTLs.

SoroProbe packages it. It dry-runs contract calls, shows you the decoded result
and the resource cost, and reports how close a contract's on-ledger state is to
expiring, in terms you can act on.

SoroProbe is **read-only**. It simulates and inspects. It never submits a
transaction, and it never needs a secret key.

```
$ soroprobe check CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4

contract CCLV77FYLRJMBGTILTKVIM76SI6JY56H7KTT47HE26UF4F265UYRDSR4

  PASS  deployed               instance found, executable is wasm
  WARN  instance_ttl           live for 58912 more ledgers (~3.4 days, until ledger 4073187)
  WARN  code_ttl               live for 24268 more ledgers (~1.4 days, until ledger 4038543)
  SKIP  simulate               no function given; pass one to probe a read-only call

result PASS
checked at ledger 4014275
```

That contract's Wasm code expires **two days before** its instance does. Reading
only one of the two would have looked fine.

## Contents

- [Versions this targets](#versions-this-targets)
- [Install](#install)
- [Quickstart](#quickstart)
- [The state expiration model](#the-state-expiration-model)
- [CLI reference](#cli-reference)
- [Argument encoding](#argument-encoding)
- [HTTP API](#http-api)
- [Configuration](#configuration)
- [Why no secret key](#why-no-secret-key)
- [Design](#design)
- [What SoroProbe deliberately does not do](#what-soroprobe-deliberately-does-not-do)

## Versions this targets

Soroban's simulation and state model change between protocol versions, so the
specifics below were verified against a live network rather than taken from
memory. Verified **2026-08-07**:

| Component | Version | Notes |
| --- | --- | --- |
| Go SDK | `github.com/stellar/go-stellar-sdk` v0.7.1 | The former `github.com/stellar/go` is **deprecated and archived**; the Go tool itself reports `module github.com/stellar/go is deprecated: Use github.com/stellar/go-stellar-sdk instead`. |
| Go | 1.25 | Required by the SDK. `GOTOOLCHAIN=auto` lets an older local `go` fetch it. |
| Stellar RPC | `simulateTransaction`, `getLedgerEntries`, `getLatestLedger` | Accessed through the SDK's `clients/rpcclient`. |
| Network | Testnet, **protocol 27** | Ledger close time measured at 5.00s/ledger over 19 consecutive ledgers. |

Two details that commonly trip up code written against older docs:

- **`simulateTransaction` no longer returns a top-level `cost` object.** The old
  `cost.cpuInsns` / `cost.memBytes` pair is gone. Resource figures now come from
  decoding the returned `transactionData` into a `SorobanTransactionData`.
- **`readBytes` was renamed `diskReadBytes`** in protocol 23. SoroProbe reports
  it under the current name.

To re-verify against the live network after a protocol upgrade, run
`make fixtures` and re-run the suite.

## Install

```bash
go install github.com/soroworks/soroprobe/cmd/soroprobe@latest
```

Or build from source:

```bash
git clone https://github.com/soroworks/soroprobe
cd soroprobe
make build      # produces ./bin/soroprobe
```

Or with Docker:

```bash
docker build -t soroprobe .
docker run --rm soroprobe inspect CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC
```

Or run the HTTP API with Docker Compose, which builds the image and serves on
`localhost:8080`:

```bash
docker compose up
```

`RPC_URL`, `NETWORK_PASSPHRASE` and `SOROVAULT_URL` are passed through from
the environment (or a `.env` file); the defaults target the public testnet, so
the stack comes up with no configuration at all.

If your local Go is older than 1.25, nothing needs to change: `GOTOOLCHAIN=auto`
is Go's default and will fetch the right toolchain on first build.

## Quickstart

Everything below works against the public testnet with no configuration. The
contract used is the testnet native-asset contract.

**Simulate a call:**

```bash
soroprobe simulate CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC decimals
```

```
contract CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC
function decimals

SUCCESS  the call would succeed

return value
  7

resource cost
  cpu instructions   127569
  disk read bytes    0
  write bytes        0
  resource fee       12279 stroops (0.0012279 XLM)
  min resource fee   12279 stroops (0.0012279 XLM)

footprint
  read  contract instance CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC (persistent)

simulated against ledger 4014077
```

**Inspect its state health:**

```bash
soroprobe inspect CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC
```

**Run a CI-friendly check:**

```bash
soroprobe check CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC --fn decimals
echo $?    # 0 pass, 1 unhealthy, 2 SoroProbe could not run
```

**Get JSON for scripting:**

```bash
soroprobe simulate CDLZ... decimals --json | jq '.cost.instructions'
```

**Keep logs readable and a report for later:**

```bash
soroprobe check CDLZ... --fn decimals --report report.json
# prints the usual text report and writes report.json, even when the check fails
```

## The state expiration model

This is the part SoroProbe exists to make legible, so it is worth stating
precisely.

Every Soroban contract instance, code entry and data entry carries a **TTL**: a
ledger sequence number recorded in an associated TTL ledger entry. Stellar RPC
surfaces it as `liveUntilLedgerSeq` on each `getLedgerEntries` result.

An entry is live while:

```
liveUntilLedgerSeq >= currentLedgerSeq
```

so the remaining lifetime, in ledgers, is `liveUntilLedgerSeq - currentLedgerSeq`.
Zero means it expires with the current ledger. Negative means it already has.

**What expiry costs depends on durability, and the difference matters:**

| Storage | On expiry | Recoverable |
| --- | --- | --- |
| **Temporary** | Deleted outright | **No — gone permanently** |
| **Persistent** | Archived | Yes, via restoration |
| **Instance** | Archived | Yes, via restoration |
| **Code** (Wasm) | Archived | Yes, via restoration |

SoroProbe says which of these applies in every report, because "expiring in two
days" means something very different for a temporary entry than a persistent one.

**How SoroProbe classifies a TTL:**

| Status | Meaning |
| --- | --- |
| `healthy` | More remaining ledgers than the warning threshold |
| `warning` | At or below the warning threshold (default 120,960 ledgers ≈ 7 days) |
| `critical` | At or below the critical threshold (default 17,280 ledgers ≈ 1 day) |
| `expired` | TTL has already lapsed; archived, or gone if temporary |
| `missing` | No such entry exists on the ledger |
| `unknown` | Entry exists but reported no TTL |

Global thresholds are configurable with `--warn-ledgers` and
`--critical-ledgers`. Optional instance, code and data thresholds override
those values independently; each kind accepts `warn` and `critical` values.
For example, use `--data-warn-ledgers` or `DATA_WARN_LEDGERS`. The config file
uses a `thresholds` object with `instance`, `code` and `data` objects, each
containing optional `warn` and `critical` ledger counts. Reports include the
effective thresholds applied to every entry.

**A note on the time estimates.** SoroProbe measures the average close time of
the 20 most recent ledgers and uses it to convert ledger counts to wall-clock
time. If RPC cannot provide that measurement, it falls back to the five-second
target and says so in the report. Treat the **ledger count as authoritative**
and the duration as a convenience.

**Three things worth knowing:**

1. **A contract's instance and its code expire independently.** A Wasm contract
   can have a comfortable instance TTL and code that expires days sooner, or the
   reverse. `inspect` and `check` always report both, because checking only one
   gives you false confidence.
2. **Instance storage shares the instance's single TTL.** Every value in it
   lives and dies with the instance entry, and one `extend_ttl` call covers all
   of them.
3. **Stellar Asset Contracts have no code entry.** They are implemented by the
   host itself, so there is nothing to expire. SoroProbe reports that explicitly
   rather than leaving a blank.

**Archived entries and simulation.** When a simulated call touches archived
entries, RPC returns a `restorePreamble`. SoroProbe surfaces this as
`RESTORE REQUIRED`, along with the fee a restoration would cost. It reports the
restoration; it does not perform one.

## CLI reference

### `soroprobe simulate <contract-id> <function> [args...]` or `simulate --file <calls.json>`

Builds an invocation, simulates it, and reports the outcome, decoded return
value, resource cost and ledger footprint.

| Flag | Description |
| --- | --- |
| `--file <path>` | Simulate named calls from a JSON file (`-` reads stdin). |
| `--report <path>` | Also write the JSON result to this file, for a CI artifact. |
| `--baseline <path>` | Compare the cost against a saved simulation report (a previous `--json` output). |
| `--max-increase <pct>` | Fail (exit 1) when a cost figure grows by more than this percentage. |
| `--repeat <n>` | Simulate the call `n` times and report the spread between the runs. |
| `--max-spread <pct>` | Fail (exit 1) when the spread across repeated runs exceeds this percentage. |

A call the contract rejects is **not** a tool error — it exits 0 and reports
`FAILED` with the host's error text. Use `check` if you want a non-zero exit.

`simulate --file` accepts a `calls` array containing `{name, contract_id,
function, args}` entries. Calls run sequentially; a call that cannot run is
reported and does not prevent later calls from running. Text output includes
the name, success, a truncated return value, instructions and fee; `--json`
returns the complete results. Unknown JSON fields are rejected.

**Cost against a baseline.** Absolute cost budgets need someone to pick
numbers. Often the useful question is relative: did this change make the call
more expensive than it was on `main`? Save a report and compare against it:

```
$ soroprobe simulate CDLZ… transfer … --json > baseline.json
$ soroprobe simulate CDLZ… transfer … --baseline baseline.json
…
cost compared to the baseline
  cpu instructions       127569 -> 131072  (+3503, +2.7%)
  disk read bytes        0 -> 0            (+0, n/a)
  write bytes            0 -> 0            (+0, n/a)
  resource fee (stroops) 12279 -> 13000    (+721, +5.9%)
```

The baseline must be for the **same contract and function**; a report for a
different call is rejected rather than compared, because its costs describe a
call that was never made.

`--max-increase` turns the comparison into a gate. The command exits **1** when
any cost figure grows by more than the threshold, which is what a CI job wants:

```
$ soroprobe simulate CDLZ… transfer … --baseline baseline.json --max-increase 10%
error: the cost grew by more than 10%
```

The percentage sign is optional, so `10` and `10%` mean the same thing. A
figure that grew from a baseline of zero has no percentage to compare
against, so it counts as an increase however large the threshold is: from
nothing to something is not a small change.

**Nondeterministic costs.** A single simulation can be a noisy sample — the
state moved, or the node was busy — and a baseline taken from one run produces
a flaky check. `--repeat` runs the same call several times and reports the
range each figure took:

```
$ soroprobe simulate CDLZ… transfer … --repeat 5
cost across 5 runs
  cpu instructions       127569..139264  (spread 11695, 9.2%)
  disk read bytes        0..0            (spread 0, n/a)
  write bytes            0..0            (spread 0, n/a)
  resource fee (stroops) 12279..13400    (spread 1121, 9.1%)
```

`--max-spread` turns that into a gate: the command exits **1** when any figure
spreads by more than the threshold across identical runs, because a check you
cannot reproduce is not a passing check. With `--repeat` **and** `--baseline`
together, the comparison uses the **maximum** of the repeated runs, so a flaky
estimate cannot slip under the threshold by luck.

### `soroprobe inspect <contract-id>`

Reads the contract's instance and code entries and reports expiration health.

| Flag | Description |
| --- | --- |
| `--key <spec>` | A contract data key to inspect, in `type:value` form. Repeatable. |
| `--key-file <path>` | A file listing data keys, one spec per line. Blank lines and `#` comment lines are ignored. |
| `--durability <d>` | Durability for `--key` lookups: `persistent` (default) or `temporary`. |
| `--estimate-extend <ledgers>` | Also price extending each reported entry's TTL by this many ledgers. |
| `--report <path>` | Also write the JSON result to this file, for a CI artifact. |

Stellar RPC reads ledger entries **by key** and offers no way to enumerate the
data entries a contract owns. Listing them all would require an indexer, which a
stateless tool has no business depending on — so data entries you care about are
named explicitly with `--key`.

**Many entries?** `--key-file` reads the same specs from a file, so a CI job
does not have to repeat a long command line:

```
# keys.txt — one spec per line, blank lines and # comment lines ignored
sym:Balance
sym:Admin
u32:7
```

```
$ soroprobe inspect C… --key-file keys.txt
```

Keys from the file and from `--key` are combined, duplicates dropped, and an
invalid line fails immediately — reported with its line number, before any
request is sent.

**What keeping it alive costs.** Knowing an entry expires in two days is half
the answer; the other half is what keeping it alive costs. `--estimate-extend`
simulates an extension of each reported entry and reports the fee:

```
$ soroprobe inspect CDLZ… --estimate-extend 1209600
contract CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC

executable stellar_asset

state entries
  instance   OK       healthy
             live for 1012931 more ledgers (~58.7 days, until ledger 5027056)
  code       n/a  built into the host, no code entry

cost to extend (simulated; nothing is submitted)
  instance               6128 stroops (0.0006128 XLM)  to ledger 5223725, 1209600 ledgers

overall OK       healthy
```

The extension is only ever **simulated** — nothing is submitted, and the
placeholder source account is enough. Each entry is priced on its own, with a
footprint holding that entry alone, so the fee is the fee for the entry you
are looking at.

An entry that **cannot** be extended says so instead of quoting a price: a
missing entry has nothing to extend, and an expired one must be restored
before it can be extended.

### `soroprobe check <contract-id>`

Combined health check, designed for CI.

| Flag | Description |
| --- | --- |
| `--fn <name>` | A read-only function to simulate as a liveness probe. |
| `--arg <spec>` | Argument for `--fn`, in `type:value` form. Repeatable. |
| `--key <spec>` | A contract data key to include in the TTL check. Repeatable. |
| `--key-file <path>` | A file listing data keys, one spec per line. Combined with `--key`. |
| `--durability <d>` | Durability for `--key` lookups. |
| `--max-instructions <n>` | Fail when simulation consumes more CPU instructions than this limit. |
| `--max-fee <stroops>` | Fail when simulation resource fee exceeds this limit in stroops. |
| `--max-write-bytes <n>` | Fail when simulation write bytes exceeds this limit. |
| `--report <path>` | Also write the JSON result to this file, for a CI artifact. |

Checks run in order: `deployed`, `instance_ttl`, `code_ttl`, any `data_ttl`, `simulate`,
then `cost` (when limits are set).

**Exit codes:**

| Code | Meaning |
| --- | --- |
| 0 | All checks passed |
| 1 | A check failed — the contract has a problem |
| 2 | SoroProbe could not run — bad input, or RPC unreachable |

1 and 2 are kept distinct so a pipeline can tell "your contract is unhealthy"
from "the tool broke".

**GitHub Actions annotations.** Inside a workflow (`GITHUB_ACTIONS` is set)
every failed or warned step also prints a workflow command, which GitHub shows
on the run summary instead of burying it in the log:

```
::error title=soroprobe CDLZ…%3A code_ttl::expired at ledger 4038543
```

`--json` suppresses annotations, since its consumer parses the document.

**Exit-code-only checks.** `--quiet` (`-q`) prints no report at all — success
and check failure are told apart by the exit code alone. Errors that make
SoroProbe itself fail (exit 2) still reach **stderr**, which is the point:
`> /dev/null` used to hide them, and `--quiet` does not. `--quiet` cannot be
combined with `--json` or `--report`.

**Checking many contracts at once.** `--file` runs every check listed in a
JSON or YAML file (`-` reads stdin), so one CI step can cover a whole deployment:

```json
{"checks": [
  {"name": "token", "contract_id": "CDLZ…", "function": "decimals"},
  {"name": "vault", "contract_id": "CCLV…", "data_keys": ["sym:Admin"]}
]}
```

```
$ soroprobe check --file checks.json
  PASS   token  instance_ttl: live for 58912 more ledgers (~3.4 days, …)
  FAIL   vault  code_ttl: expired at ledger 4038543

result 1 passed, 1 failed, 0 could not run
```

Each entry takes `name` (defaults to the contract ID, and must be unique),
`contract_id`, `function`, `args`, `data_keys`, `data_durability` and `timeout`.
Unknown keys are rejected, so a typo cannot silently drop a step. Checks run one
at a time by default to stay inside public RPC rate limits; `--concurrency N`
runs up to N checks at once for endpoints that allow it. Results remain in file
order, and cancellation stops checks waiting to start. One check that cannot run
is reported and the rest continue. The exit code is 2 if any check could not
run, else 1 if any failed, else 0.

`timeout` bounds a single check, as a Go duration string (`"45s"`); a check that
outlives it is reported as one that could not run, and the batch continues.
`--check-timeout` sets the default for entries that name none — a per-entry
`timeout` always wins — so one slow contract cannot eat the whole CI step:

```json
{"checks": [
  {"name": "token", "contract_id": "CDLZ…", "function": "decimals", "timeout": "10s"},
  {"name": "vault", "contract_id": "CCLV…", "data_keys": ["sym:Admin"]}
]}
```

With `--check-timeout 30s`, `token` is still bounded at 10s and `vault` at 30s.

A **TTL warning does not fail the check.** It is a signal to extend the entry,
not a reason to break a build. Only `critical`, `expired` and `missing` fail.

`--fn` is optional by design. SoroProbe cannot discover a contract's exported
functions from the ledger, and guessing a name would produce a failure that
looks like a broken contract but is really a broken guess. Without `--fn` the
simulation step is honestly reported as `SKIP`.

### `soroprobe serve`

Runs the HTTP API. It binds `127.0.0.1:8080` by default; `--addr` overrides
`HTTP_ADDR`. Set `API_TOKEN` (or `--api-token`) to require a bearer token on
the `/v1` routes — see [Authentication](#authentication).

`--inspect-cache-ttl` (env `INSPECT_CACHE_TTL`) lets the server reuse an inspect
result for the same contract, durability and key set for that long. It is **off
by default**: TTLs change at most once per ledger (about five seconds), so a
cached answer can lag a ledger boundary, and on-demand computation keeps every
response current. A value of `3s`–`5s` is reasonable for dashboards that poll
the same contracts. A cached response still reports the ledger it was computed
at in `latest_ledger`; simulations are never cached. The cache is bounded to 256
entries, and expired entries are dropped on read.

### Global flags

`--json`, `--config`, `--profile`, `--rpc-url`, `--network-passphrase`, `--source-account`,
`--log-level`, `--log-format`, `--timeout`, `--warn-ledgers`,
`--critical-ledgers`, `--sorovault-url`.

## Argument encoding

Arguments use a `type:value` form:

```bash
soroprobe simulate CDLZ... transfer \
  addr:GABC...  addr:CDEF...  i128:10000000
```

| Type | Example |
| --- | --- |
| `bool` | `bool:true` |
| `void`, `null` | `void` |
| `u32`, `i32` | `u32:5`, `i32:-5` |
| `u64`, `i64` | `u64:100` |
| `u128`, `i128` | `i128:-10000000`, `i128:10.5@7` |
| `u256`, `i256` | `u256:12345` |
| `timepoint`, `duration` | `timepoint:1700000000` |
| `sym`, `symbol` | `sym:transfer` |
| `str`, `string` | `str:"hello world"` |
| `bytes` | `bytes:deadbeef`, `bytes:0xdeadbeef` |
| `addr`, `address` | `addr:GABC...`, `addr:CDEF...` |
| `vec` | `vec:["u32:1","u32:2"]`, `vec:[1, 2]` |
| `map` | `map:[["sym:a","u32:1"],["sym:b","u32:2"]]` |

### Token amounts

Token functions take amounts in **base units**, so 10 XLM-equivalent of a
7-decimal token is `100000000`, and miscounting the zeros sends the wrong
amount to a simulation. An integer argument can carry a decimal scale, written
as an `@` followed by the token's decimal count:

```bash
soroprobe simulate CDLZ... transfer addr:GABC... addr:CDEF... i128:10.5@7
```

| Argument | Means | Encoded as |
| --- | --- | --- |
| `i128:10.5@7` | 10.5 tokens of a 7-decimal token | `105000000` |
| `i128:10@7` | 10 tokens of a 7-decimal token | `100000000` |
| `i128:-2.5@7` | minus 2.5 tokens | `-25000000` |
| `i128:0.0000001@7` | one base unit | `1` |
| `i128:10000000` | 10000000 base units, unscaled | `10000000` |

The conversion is **exact**: the integer and fractional parts are scaled with
integer arithmetic, never with floating point, so no amount is ever off by a
rounding error. An amount with more fractional digits than the token has
decimals — `i128:10.12345@2` — is **rejected** rather than rounded, because a
rounded amount is a different amount. Negative amounts and zero work.

The scale is part of the argument, so it needs a type prefix: a bare `10.5@7`
is not an amount, it is a string.

**Collections** are written as JSON, the one nesting syntax that survives a
shell intact — quote the whole argument:

```bash
soroprobe simulate CDEF... set_weights 'map:[["sym:alice", "u32:3"], ["sym:bob", "u32:1"]]'
```

Each string element is itself an argument spec, so every type above works
inside a collection, including nested `vec`/`map`. A JSON number, `true`,
`false` or `null` stands for the bare literal of that spelling and is
inferred the same way; a nested array is a nested `vec`. Map entries are kept
in the order written, and Soroban rejects a map whose keys are not sorted
ascending and unique, so write them in order.

Collections may nest up to 32 levels deep. Deeper nesting is turned away
rather than parsed, because every level is read again on the way down.

**Inference.** A bare value with no prefix is inferred: `true`/`false` become
bool, `void`/`null` become void, a valid `G...`/`C...` address becomes an
address, a run of digits becomes **i128**, and anything else becomes a symbol if
it fits Soroban's symbol rules (≤32 chars of `[a-zA-Z0-9_]`) or a string
otherwise.

Prefer explicit prefixes. Bare integers infer `i128` because it is the widest
common integer type in Soroban token interfaces, but a contract expecting `u32`
will reject it with a confusing host error.

### Typing arguments from SoroVault

Inference is a guess. If you run a [SoroVault](https://github.com/soroworks/sorovault)
registry, point SoroProbe at it and the guess goes away:

```bash
export SOROVAULT_URL=http://localhost:8080
soroprobe simulate CDZZ... get_proposal 3
```

```
contract CDZZ...
function fn get_proposal(proposal_index: u32) -> Proposal
encoded  u32:3
```

SoroProbe fetches the contract's interface from
`GET /api/contracts/{id}?network=…` and, for each argument **without** an
explicit prefix, uses the type the function declares — so `3` above is a
`u32`, not an `i128`. An explicit prefix always wins. With the interface in
hand SoroProbe also refuses, before simulating anything:

- a function the contract does not export, listing the ones it does, and
- the wrong number of arguments, quoting the signature.

Both are input errors (exit 2 on the CLI, 400 from the API), because the
simulation could only fail and its host error would be less helpful.

If the contract is not in the registry, or the registry cannot be reached,
SoroProbe falls back to inference and says so in a `note:` line (`abi_note`
in JSON), rather than failing a probe that would otherwise work. The network
is matched by passphrase, so a mainnet probe never picks up a testnet
interface.

Collections are typed element by element: for a `Vec<u32>` parameter, `[1, 2]`
becomes `vec:["u32:1","u32:2"]`, and a `Map<Symbol, u32>` given as
`[["a", 10]]` gets typed keys and values, recursively. Tuples and
user-defined types have no literal form and are passed through unchanged.

**Results** are decoded to JSON-friendly values. Integers wider than 32 bits
become decimal **strings**, not JSON numbers — a `u64` or `i128` cannot survive
a round trip through a float64, and silently mangling a token balance would be
worse than a quoted string. Bytes become hex; addresses become strkey.

## HTTP API

`soroprobe serve` exposes the CLI over HTTP for CI systems and other services.
It is read-only; there is no route that submits anything.

**It listens on `127.0.0.1:8080` by default.** SoroProbe is read-only, but an
instance reachable from the network still spends the operator's RPC quota on
whichever RPC endpoint it is pointed at, so serving wider than the local
machine is an opt-in. Widen the address deliberately:

```bash
soroprobe serve --addr 0.0.0.0:8080      # or HTTP_ADDR=0.0.0.0:8080
```

The Docker image sets `HTTP_ADDR=:8080` itself, since loopback inside a
container would reach nothing.

Set `CORS_ORIGINS` to a comma-separated list of exact browser origins, such as
`https://dashboard.example,https://admin.example`, to enable CORS. Unset means
no CORS headers. Preflight requests are answered only for listed origins.

### Authentication

The API is unauthenticated by default, which is fine on a private network.
Exposed more widely, anyone who can reach it can make it spend the operator's
RPC quota — a paid provider, often — on arbitrary simulations. Set a token to
stop that:

```bash
export API_TOKEN="$(openssl rand -hex 32)"
soroprobe serve --addr 0.0.0.0:8080       # or: serve --api-token <token>
```

With a token set, every route under `/v1` requires
`Authorization: Bearer <token>`; anything else is a **401** with a
`WWW-Authenticate` header. `/healthz` never requires auth, so a liveness probe
needs no credential. Unset keeps today's open behaviour. Two details are
enforced rather than advised:

- **Tokens shorter than 16 characters are rejected at startup.** A token short
  enough to guess is not meaningfully different from no token at all.
- **Comparison is constant-time**, so response timing cannot be used to
  discover the token one character at a time.

```bash
curl -s -H "Authorization: Bearer $API_TOKEN" \
  localhost:8080/v1/inspect/CDLZ… | jq
```

### `GET /healthz`

Liveness. Deliberately does **not** call out to the network, so the probe does
not fail because an upstream RPC endpoint blipped.

```json
{"status": "ok"}
```

### `GET /readyz`

Readiness. Unlike `/healthz`, it calls the configured RPC endpoint: **200**
when the endpoint reports healthy and serves the configured network
passphrase, **503** with a JSON reason otherwise. An orchestrator uses it to
take an instance out of rotation while its upstream is down, without
restarting the process — which is exactly what `/healthz` must not do.

```json
{"status": "ready"}
```

```json
{"status": "unready", "reason": "rpc unreachable: getHealth: connection refused"}
```

The result — success or failure — is cached for a few seconds, so a chatty
orchestrator does not multiply RPC traffic. The cache is server-side only; the
response itself is marked `Cache-Control: no-store`, because its answer
describes live upstream state.

### `POST /v1/simulate`

```bash
curl -s localhost:8080/v1/simulate -d '{
  "contract_id": "CDLZFC3SYJYDZT7K67VZ75HPJVIEUVNIXF47ZG2FB2RMQQVU2HHGCYSC",
  "function": "decimals",
  "args": []
}' | jq
```

Optional `baseline` and `max_increase_percent` compare the cost against a saved
report, exactly as `simulate --baseline` does. The baseline has the shape of a
`--json` report:

```bash
curl -s localhost:8080/v1/simulate -d '{
  "contract_id": "CDLZ…",
  "function": "decimals",
  "baseline": {"contract_id": "CDLZ…", "function": "decimals", "cost": {"instructions": 127569}},
  "max_increase_percent": 10
}' | jq
```

A baseline for a different contract or function is a **400**: it describes a
call that was never made.

### `GET /v1/inspect/{contract}`

Query parameters: `key` (repeatable), `durability`, `estimate_extend`.

`estimate_extend` prices the extension of each reported entry's TTL by that
many ledgers, exactly as `inspect --estimate-extend` does.

### `GET /v1/check/{contract}`

Query parameters: `fn`, `arg` (repeatable), `key` (repeatable), `durability`, `max_instructions`, `max_fee` (stroops), `max_write_bytes`.

### `POST /v1/checks`

Several checks in one request — the same body `soroprobe check --file` reads.
Returns `ok`, `errored`, and one item per check. Each entry may set `timeout`
(a Go duration string) to bound that check alone; it defaults to none. Limited
to 20 checks per request, because the request shares one deadline and checks
run sequentially; split larger batches or use the CLI.

### Status codes

| Code | Meaning |
| --- | --- |
| 200 | The probe ran. **Read `success` / `ok` in the body for the verdict.** |
| 400 | Bad input — malformed contract id, bad argument, unknown durability |
| 401 | `API_TOKEN` is set and the bearer token is missing or wrong |
| 404 | No such route |
| 502 | Upstream RPC failed |
| 503 | `/readyz` only: the RPC endpoint is unreachable, unhealthy, or on another network |

A contract that fails its check still returns **200**: the request succeeded, it
just carries bad news. Returning 5xx would conflate an unhealthy contract with a
broken service, and monitoring cannot tell those apart after the fact.

### Response headers

Every response carries `X-Content-Type-Options: nosniff`, so a browser that is
pointed at the API cannot be tricked into interpreting a JSON body as HTML.

Routes under `/v1` — and `GET /readyz` — also carry `Cache-Control: no-store`:
their answers describe live on-chain state, and a cached health result would
be worse than no result. `/healthz` is exempt — it holds no chain state and is
meant to be polled cheaply. The optional in-process inspect cache
(`--inspect-cache-ttl`) is separate: it serves a recent answer for at most the
configured few seconds and still reports the ledger it was computed at, but
clients are never told to cache it.

## Configuration

Precedence, lowest to highest: **defaults → top-level config file keys → profile → environment → flags**.

| Environment variable | Flag | Default |
| --- | --- | --- |
| `RPC_URL` | `--rpc-url` | `https://soroban-testnet.stellar.org` |
| `NETWORK_PASSPHRASE` | `--network-passphrase` | `Test SDF Network ; September 2015` |
| `SOURCE_ACCOUNT` | `--source-account` | all-zero placeholder |
| `HTTP_ADDR` | `--addr` (on `serve`) | `127.0.0.1:8080` |
| `CORS_ORIGINS` | — | unset; no CORS headers |
| `API_TOKEN` | `--api-token` (on `serve`) | unset — `/v1` routes are open |
| `LOG_LEVEL` | `--log-level` | `info` |
| `LOG_FORMAT` | `--log-format` | `text` |
| `RPC_TIMEOUT` | `--timeout` | `30s` |
| `INSPECT_CACHE_TTL` | `--inspect-cache-ttl` (on `serve`) | `0s` (off) |
| `WARN_LEDGERS` | `--warn-ledgers` | `120960` (≈7 days) |
| `CRITICAL_LEDGERS` | `--critical-ledgers` | `17280` (≈1 day) |
| `INSTANCE_WARN_LEDGERS`, `INSTANCE_CRITICAL_LEDGERS` | `--instance-warn-ledgers`, `--instance-critical-ledgers` | global thresholds |
| `CODE_WARN_LEDGERS`, `CODE_CRITICAL_LEDGERS` | `--code-warn-ledgers`, `--code-critical-ledgers` | global thresholds |
| `DATA_WARN_LEDGERS`, `DATA_CRITICAL_LEDGERS` | `--data-warn-ledgers`, `--data-critical-ledgers` | global thresholds |
| `SOROVAULT_URL` | `--sorovault-url` | unset — [type arguments from SoroVault](#typing-arguments-from-sorovault) |
| `SOROPROBE_CONFIG` | `--config` | `./soroprobe.json` (then `./soroprobe.yaml`, `./soroprobe.yml`) if present |
| `SOROPROBE_PROFILE` | `--profile` | unset |

To type arguments from a contract's on-chain interface instead of SoroVault,
pass `--abi-from-chain`. Stellar Asset Contracts have no Wasm interface and
continue with inferred argument types, with a note in the result.

Logs go to **stderr** in both formats, so redirecting stdout (or consuming
`--json` output) never mixes a log line into the document being parsed.
`LOG_FORMAT=json` (or `--log-format json`) emits one JSON object per line —
filterable by field (`contract`, `status`) in CI and log aggregators instead
of regex-parsed. `text`, the default, keeps the readable `key=value` lines.

[.env.example](.env.example) documents every variable. SoroProbe reads the
**environment**, not `.env` itself, so load it yourself:

```bash
set -a && . ./.env && set +a
docker run --env-file .env soroprobe check <contract>
```

A config file, by contrast, is read directly. JSON and YAML formats (`soroprobe.json`, `soroprobe.yaml`, `soroprobe.yml`) are supported, with snake_case keys (unknown keys are rejected in both formats). When no path is specified, SoroProbe looks for `soroprobe.json`, then `soroprobe.yaml`, and finally `soroprobe.yml`.

### Profiles

Config files can define named `profiles` for different networks (such as `testnet`, `mainnet`, or `local`). Select a profile using `--profile <name>` or the `SOROPROBE_PROFILE` environment variable. An unknown profile name produces an error listing the available profiles.

In JSON:

```json
{
  "rpc_url": "https://soroban-testnet.stellar.org",
  "warn_ledgers": 120960,
  "critical_ledgers": 17280,
  "inspect_cache_ttl": "0s",
  "profiles": {
    "mainnet": {
      "rpc_url": "https://rpc.mainnet.stellar.org",
      "network_passphrase": "Public Global Stellar Network ; September 2015",
      "warn_ledgers": 172800,
      "critical_ledgers": 34560
    }
  }
}
```

Or in YAML:

```yaml
# SoroProbe configuration
rpc_url: https://soroban-testnet.stellar.org
warn_ledgers: 120960
critical_ledgers: 17280

profiles:
  mainnet:
    rpc_url: https://rpc.mainnet.stellar.org
    network_passphrase: "Public Global Stellar Network ; September 2015"
    warn_ledgers: 172800
    critical_ledgers: 34560
```

## Why no secret key

Simulation does not require a signature. The RPC server does not verify one, and
it never broadcasts the transaction.

Building a transaction does require a **source account**, so SoroProbe asks for
a public key — and only a public key. `Validate` rejects anything that is not a
valid `G...` ed25519 public key, so pasting a secret key in fails immediately
rather than quietly working.

The account does not need to exist or hold a balance. The default is an all-zero
placeholder, which is why the quickstart runs with no setup at all. Set
`SOURCE_ACCOUNT` to a real account only when a contract authorizes on the
invoker's address and you want the simulation to reflect that.

The transaction SoroProbe builds is unsigned, uses sequence number 0, and is
never submitted. There is a test asserting the envelope carries no signatures.

## Design

```
cmd/soroprobe     cobra CLI and output rendering
internal/config   defaults, config file and environment resolution
internal/stellar  Stellar RPC client behind an interface, ledger keys, tx building
  └── stellartest fixture-backed fake + the recorder that captures fixtures
internal/scval    ScVal encode/decode behind an interface, with a type registry
internal/abi      contract interfaces from SoroVault, used to type arguments
internal/health   TTL interpretation: thresholds, statuses, durability semantics
internal/probe    simulate / inspect / check orchestration
internal/api      chi HTTP handlers mirroring the CLI
```

Two seams exist so the core is testable and replaceable:

- **`stellar.Client`** is an interface over the RPC methods SoroProbe uses. The
  live implementation wraps the SDK client; tests use a fixture-backed fake.
- **`scval.Codec`** is an interface over argument encoding and result decoding.
  The default `Registry` maps type names to `EncodeFunc`s, so supporting a new
  type is one function and one `Register` call.

The CLI and the HTTP API both render the same result structs from `probe`, so
the two interfaces cannot drift apart.

**Tests never touch the network.** They replay responses recorded from the live
testnet into `internal/stellar/stellartest/testdata`. The fake resolves ledger
entries by key and simulations by decoding the transaction envelope it is
handed, so a test exercising the fake also exercises the real ledger-key and
transaction-building code rather than trusting it. Refresh fixtures with
`make fixtures`.

## What SoroProbe deliberately does not do

- **Submit transactions.** SoroProbe is simulation and inspection only. There is
  no code path that broadcasts, and no route in the API that could.
- **Deploy contracts.**
- **Restore archived entries.** It reports that a restore is required, and the
  fee, and leaves the decision to you.
- **Serve a web UI.**
- **Persist anything.** SoroProbe is stateless and queries the chain live. No
  database, by design.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). Adding an ScVal type or an RPC method is
a small, well-bounded change; both are documented there.

## License

Apache-2.0. See [LICENSE](LICENSE).
