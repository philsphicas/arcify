# arcify

[![CI](https://github.com/philsphicas/arcify/actions/workflows/ci.yml/badge.svg)](https://github.com/philsphicas/arcify/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/philsphicas/arcify)](https://goreportcard.com/report/github.com/philsphicas/arcify)
[![GitHub Release](https://img.shields.io/github/v/release/philsphicas/arcify)](https://github.com/philsphicas/arcify/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

One-shot Azure Arc enrollment for an existing Azure VM.

```sh
arcify /subscriptions/<sub>/resourceGroups/<rg>/providers/Microsoft.Compute/virtualMachines/<vm>
```

## Why

Standard Arc onboarding is built for non-Azure machines; Arc-enrolling an Azure VM is a deliberately gated test/sandbox scenario. arcify automates the gate-flipping, keypair exchange, and `runCommand` dispatch so you can stand up Arc-enabled VMs in one command.

## Install

```sh
go install github.com/philsphicas/arcify@latest
```

Or grab a pre-built binary for `linux`, `darwin`, or `windows` × `amd64`/`arm64` from the [Releases](https://github.com/philsphicas/arcify/releases) page.

## Usage

```sh
arcify <vm-arm-id> [flags]
```

Run `arcify --help` for the full flag list. Highlights:

- `--arc-{subscription,rg,name,location}` — override any field of the Arc resource; defaults match the VM
- `--wait <duration>` — clock-time budget for the in-VM script + `Connected` verification (default `5m`)
- `--no-wait` — dispatch the runCommand and exit immediately (the ARM ID is still printed); script output is unrecoverable
- `--force` — recreate if an Arc machine already exists at the target
- `--dry-run` — print the plan without touching ARM
- `--precreate` — only create the Arc resource and emit a connection payload (private key + identity) to stdout; see [Precreate-only mode](#precreate-only-mode)

Cross-tenant is supported: the Arc resource can live in a different tenant than the VM.

### Precreate-only mode

For scenarios where arcify should _just_ pre-create the Arc resource and hand the connection material off to a different consumer (e.g. a container or service that runs `azcmagent connect existing` itself):

```sh
umask 077
arcify --precreate \
    --arc-subscription "$SUB" \
    --arc-rg "$RG" \
    --arc-name "$NAME" \
    --arc-location "$LOC" > arc.env
```

No VM is contacted; arcify generates the keypair, creates `Microsoft.HybridCompute/machines/$NAME` with the public key, and writes the consumer's input — including the private key — to stdout. Default output format is Docker `--env-file` (`KEY=VALUE` per line, no shell escaping); pass `--output json` for a JSON object with the same fields. The payload includes the subscription, resource group, resource name, location, tenant ID, Arc resource ID, generated VM ID, and the matching base64-encoded RSA private key. Cross-tenant: pass `--arc-tenant <id>` to skip the tenant lookup against the Arc subscription.

> ⚠️ **Secret hygiene.** The payload contains the matching private key. Anyone who reads stdout (or the file you redirect it to) can connect to the Arc resource. Set `umask 077` before redirecting, don't commit the file, and avoid capturing it in CI logs.

### Supported guests

- **Linux:** Ubuntu LTS, Azure Linux 3
- **Windows:** Server 2016+, Windows 10/11

### Prerequisites

- The VM must have the **Azure VM agent** installed and `AllowExtensionOperations` enabled (the default on all Marketplace images).
- The guest needs outbound network access to `aka.ms`, the package mirror for its distro, and the standard Arc data plane endpoints. See the [Azure Arc network requirements](https://learn.microsoft.com/azure/azure-arc/servers/network-requirements) for the full list.
- `--dry-run` still authenticates to ARM and reads the VM and tenant — it just doesn't write or run anything.

## Output

arcify follows the small-Unix-utility convention so it composes cleanly with the rest of your toolchain:

- **stdout** — on success, the Arc machine's ARM ID (a single line) in default mode, or the connection payload (env-file or JSON) in `--precreate` mode. Empty on failure or `--dry-run`.
- **stderr** — human-readable progress, errors, and (on failure) a dump of the in-VM script's captured output for diagnosis.

arcify uses the action-style `runCommand` API (`POST .../runCommand`), which executes the script through the VM agent without creating a tracked ARM child resource — there's nothing to clean up afterward, which keeps the tail latency on a successful enrollment to just the time it takes to verify `Connected` status.

If the script fails or the agent never reaches `Connected`, arcify leaves the Arc machine resource in place and prints its ARM ID to stderr so you can decide what to do (retry with `--force`, `az resource delete --ids ...`, etc.). It does **not** auto-roll-back.

```sh
ARC_ID=$(arcify "$VM_ID" --wait 10m) && az resource show --ids "$ARC_ID"
```

## Authentication

arcify uses [`DefaultAzureCredential`](https://learn.microsoft.com/azure/developer/go/sdk/authentication-overview): environment vars → managed identity → `az` cache → `azd` cache → workload identity → browser. If you're already `az login`'d, it just works.

The RSA keypair arcify generates lives only in process memory. In default mode the public half is uploaded to the Arc resource at create time, the private half is handed to the in-VM script as a `runCommand` parameter, and nothing is ever written to disk. In `--precreate` mode the private half is written to stdout for the downstream consumer to ingest, so handle the redirect file with the same care you'd give any other long-lived credential.

> Note: the action-style runCommand API has no `protectedParameters` channel, so the private key travels in the `parameters` field. It's still TLS-encrypted in transit and never persists in ARM (action-style runCommand doesn't create a tracked resource), but it may appear unredacted in client-side logs that capture the outbound request body.

## RBAC

The identity needs **Virtual Machine Contributor** on the VM's resource group and **Azure Connected Machine Onboarding** on the Arc resource group — or simply **Contributor** on both.

## Exit codes

| Code | Meaning                                                             |
| ---- | ------------------------------------------------------------------- |
| 0    | Success (Arc `Connected` confirmed, or `--no-wait` accepted)        |
| 1    | Operational failure — see stderr for the error and any leftover IDs |
| 2    | Invalid arguments                                                   |

## License

MIT — see [LICENSE](LICENSE).
