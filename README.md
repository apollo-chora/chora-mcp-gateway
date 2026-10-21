# chora-mcp-gateway

> The MCP (Model Context Protocol) front door: resolves a partner API key to a tenant and
> a tool allowlist, then fans the tool call out to the owning domain service.

Go, HTTP/JSON only. No database, no migrations, no event bus — this is a stateless
authentication and routing layer in front of five upstreams.

## What it is

An MCP server for external agents. A partner presents a JSON-RPC 2.0 request with an
`X-API-Key` header; the gateway resolves that key to a partner, a tenant and a set of
permitted tools, then proxies the call to whichever service owns the tool.

It serves exactly five things over HTTP: three health endpoints and two MCP endpoints.

### The five tools, and what actually works

The catalogue is small and two of the five are functional. The other three fail loudly by
design, which is worth knowing before you debug them:

| Tool | Scope metadata | Behaviour |
| --- | --- | --- |
| `course_catalog` | `mcp:read` | **works** — calls `GET {MCP_UPSTREAM_DELIVERY}/api/courses` |
| `learning_path_query` | `mcp:read` | **works** — calls `GET {MCP_UPSTREAM_CONSUMPTION}/api/learning-paths` |
| `atom_search` | `mcp:read` | fails loudly — the upstream route requires a learner GCID the gateway does not supply |
| `governance_check` | `mcp:read` | fails loudly — same reason, the upstream needs a GCID |
| `familiar_nudge` | `mcp:write` | fails loudly — no upstream HTTP nudge route exists |

`requiredScope` is **metadata only**. Authorization is decided entirely by whether the
tool's name appears in the partner's allowlist, so a `mcp:write` label on a tool grants
nothing by itself.

`course_catalog` accepts optional numeric `limit` and `offset`.

`learning_path_query` accepts an optional `learner_gcid`, but it is applied **locally**:
the gateway fetches the tenant's learning paths and filters the returned items by
`owner_gcid`. It does not narrow the upstream request, so the call can succeed and still
return nothing for a learner who does have paths — if the upstream page did not contain
them.

### Error shapes

Four different failures, and they look different on purpose:

| Situation | Response |
| --- | --- |
| tool executes but the upstream fails | JSON-RPC **result** with `isError: true` and no citations |
| unknown tool name, on `tools/call` | JSON-RPC error `-32002` |
| known tool, caller not permitted, on `tools/call` | **HTTP 403** with code `TOOL_NOT_ALLOWED` |
| tool not in the caller's allowlist, on `tools/list` | simply **omitted** from the listing — no error |
| missing or unresolvable `X-API-Key` | **HTTP 401**, decided before any tool lookup |

That last row is the one you will meet first in a fresh stack — see
[Credentials](#credentials).

## Prerequisites

| Route | What you need |
| --- | --- |
| In the stack | A working `chora-stack` deployment. The five upstreams come from it. |
| Standalone | Go **1.26.1 or newer**, plus a reachable API-key resolver — either the A2A gateway or a development seed. The process refuses to start with neither. |
| To call a tool | A partner API key with the tool in its allowlist. Nothing works with an invented key. |

## Credentials

This service holds **no provider credentials** — it calls no model and no search API. Its
credential story is entirely about the *inbound* API key a partner presents.

| Value | Where it comes from | Variable |
| --- | --- | --- |
| API-key resolver | the stack's A2A gateway | `MCP_UPSTREAM_A2A_GATEWAY` |
| Development seed keys | **you supply them**; local only | `MCP_DEV_SEED_API_KEYS` |
| Upstream base URLs | the Compose network; not secrets | `MCP_UPSTREAM_{CREATION,DELIVERY,CONSUMPTION,GOVERNANCE}` |

**In this stack, no key works until you create a partner.** `compose.yml` sets
`MCP_UPSTREAM_A2A_GATEWAY` and does **not** set `MCP_DEV_SEED_API_KEYS`, so the only
accepted keys are ones the A2A gateway knows about. A `dev-key` or any invented value
returns **401** — verified live.

To get a working key, register and approve a partner through `chora-a2a-gateway`, which
mints the AGID and API key on approval:

- `POST /partners/register`
- `POST /admin/partners/{id}:approve`

The resolver then returns the partner's AGID, tenant id, tool allowlist and active state,
and the gateway propagates the tenant upstream as `X-Tenant-Id`. An empty tenant id makes
tenant-scoped tools fail rather than silently defaulting.

**The allowlist arrives in a field named `allowed_scopes`, not `allowed_tools`.** The wire
name is a legacy of an earlier contract, and the values are tool names despite it. A
resolver response carrying only `allowed_tools` decodes as an empty list, and the partner
then sees no tools at all. `chora-a2a-gateway`'s registration endpoint takes
`allowed_tools` and surfaces it as `allowed_scopes` — preserve that mapping when changing
either side.

For local work without A2A, set the seed instead:

```sh
export MCP_DEV_SEED_API_KEYS='dev-key:0197...:00000000-0000-7000-8000-000000000001:course_catalog,learning_path_query'
```

The format is `key:agid:tenant_id:tool1,tool2`. It is a development shortcut — the seed
lives in the process environment and is not a partner record.

**The process refuses to start** when neither the resolver URL nor the seed is configured.
That is deliberate: an MCP gateway with no way to authenticate anyone is not useful.

The two are **not** a fallback pair. When `MCP_UPSTREAM_A2A_GATEWAY` is set it always wins,
even if a seed is also present — and if A2A is configured but unreachable, the process
still starts and every key resolution fails at request time. There is no automatic switch to
the seed during an A2A outage; unset the URL if you want the seed used.

## Run it in the stack

`compose.yml` does three things:

1. Builds the image from the sibling checkout at `../chora-mcp-gateway`.
2. Starts it once `creation`, `delivery`, `consumption`, `governance` and `a2a-gateway`
   have **started** (`service_started`, not healthy — a peer that starts and then crashes
   leaves the corresponding tool failing rather than blocking this service).
3. Publishes no host port; it `expose`s `8080` only.

```sh
cd chora-stack
docker compose up -d mcp-gateway
docker compose ps mcp-gateway
```

What Compose sets: `PORT`, `CHORA_SOURCE_PROJECT`, five `MCP_UPSTREAM_*` URLs — four domain
upstreams for tool dispatch plus `MCP_UPSTREAM_A2A_GATEWAY` for the resolver — and
`OTEL_EXPORTER_OTLP_ENDPOINT`. Nothing else: no database DSN, no NATS, and no
`MCP_DEV_SEED_API_KEYS`.

Probe it from inside the network. **There is no host port**, so `curl localhost:8096` (or
any host-side port) will not reach it, whatever older examples suggest:

```sh
docker compose exec -T nats wget -qO- http://mcp-gateway:8080/readyz
```

```json
{"service":"chora-mcp-gateway","status":"ok"}
```

Note that `readyz` answers `"status":"ok"` — the same body as `healthz`, not `"ready"`. It
reports that the process is serving, not that its upstreams are reachable.

## Run it standalone

```sh
git clone https://github.com/apollo-chora/chora-mcp-gateway.git
cd chora-mcp-gateway
go build -o mcp-gateway ./cmd/server
```

Then start it with either a resolver or a seed:

```sh
export PORT=8080
export MCP_DEV_SEED_API_KEYS='dev-key:01970000-0000-7000-b000-000000000001:00000000-0000-7000-8000-000000000001:course_catalog'
export MCP_UPSTREAM_DELIVERY=http://localhost:18080   # NOT 8080 — that is this gateway
./mcp-gateway
```

Point the upstreams at real services on **different** ports. Setting
`MCP_UPSTREAM_DELIVERY=http://localhost:8080` while the gateway itself listens on `8080`
makes `course_catalog` call its own listener, which will not round-trip.

Without one of the two resolver settings the process exits at startup. The binary does not
read `.env`; export the values.

Container:

```sh
docker build -t chora-mcp-gateway .
```

CI publishes multi-architecture images for `linux/amd64` and `linux/arm64` on pushes to
`main` and on version tags.

## Configuration

| Variable | Default | Purpose |
| --- | --- | --- |
| `PORT` | `8080` | HTTP listen port |
| `CHORA_ENV` | — | deployment environment label |
| `CHORA_SOURCE_PROJECT` | `chora-local` | source/event project label |
| `MCP_UPSTREAM_A2A_GATEWAY` | unset | A2A base URL used to resolve API keys |
| `MCP_UPSTREAM_DELIVERY` | unset | `chora-delivery` base URL |
| `MCP_UPSTREAM_CONSUMPTION` | unset | `chora-consumption` base URL |
| `MCP_UPSTREAM_CREATION` | unset | `chora-creation` base URL |
| `MCP_UPSTREAM_GOVERNANCE` | unset | `chora-governance` base URL |
| `MCP_DEV_SEED_API_KEYS` | unset | local-only seed, `key:agid:tenant_id:tool1,tool2` |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | stdout | OTLP/gRPC trace endpoint |

There is deliberately **no** `MCP_UPSTREAM_MODEL_BROKER` and **no** `model_broker_invoke`
tool. Older documentation describing either is stale.

## Verify it works

### It is up

```sh
docker compose exec -T nats wget -qO- http://mcp-gateway:8080/health
docker compose exec -T nats wget -qO- http://mcp-gateway:8080/healthz
docker compose exec -T nats wget -qO- http://mcp-gateway:8080/readyz
```

All three accept `GET` and all three return:

```json
{"service":"chora-mcp-gateway","status":"ok"}
```

### Authentication is actually enforced

```sh
docker compose exec -T nats wget -qO- -S \
  --header='X-API-Key: dev-key' \
  --header='Content-Type: application/json' \
  --post-data='{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://mcp-gateway:8080/mcp/tools/list
```

```text
HTTP/1.1 401 Unauthorized
```

That is the **pass** in a fresh stack: it proves the resolver path is wired and rejecting an
unknown key. It does not prove *which* authenticator answered — with
`MCP_UPSTREAM_A2A_GATEWAY` set, A2A takes precedence even if a seed is also configured, so
a `200` against a real key means A2A resolved it, not that the seed was used.

### A real key returns the allowlisted tools

Once you have minted a partner key through `chora-a2a-gateway`:

```sh
docker compose exec -T nats wget -qO- \
  --header="X-API-Key: <minted key>" \
  --header='Content-Type: application/json' \
  --post-data='{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://mcp-gateway:8080/mcp/tools/list
```

The response lists only the tools in that partner's allowlist — a partner seeded with
`course_catalog` alone must not see `learning_path_query`.

### A working tool round-trips

```sh
docker compose exec -T nats wget -qO- \
  --header="X-API-Key: <minted key>" \
  --header='Content-Type: application/json' \
  --post-data='{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"course_catalog","arguments":{"limit":5}}}' \
  http://mcp-gateway:8080/mcp/tools/call
```

Composed course data is the pass. `isError: true` with no citations means the upstream
call failed — check that upstream's logs, not this one's.

## Troubleshooting

- **Every MCP call returns `401`** — expected in a fresh stack. There is no dev seed and no
  partner key yet. Register and approve a partner through `chora-a2a-gateway`.
- **`curl localhost:<port>` from the host fails** — the service publishes no host port.
  Probe from inside the network.
- **The process exits at startup** — neither `MCP_UPSTREAM_A2A_GATEWAY` nor
  `MCP_DEV_SEED_API_KEYS` is set. That refusal is deliberate.
- **`403 TOOL_NOT_ALLOWED` for a tool you can see listed** — the allowlist and the tool name
  disagree. The allowlist is the only authorization input; `requiredScope` is metadata.
- **`-32002`** — the tool name is not in the catalogue at all. Check the spelling against
  the five-tool table above.
- **`atom_search` or `governance_check` fails** — expected. Their upstream routes need a
  learner GCID that the gateway does not send. This is not a misconfiguration on your side.
- **`familiar_nudge` fails** — expected. There is no upstream HTTP nudge route to call.
- **A tool succeeds but the tenant data looks wrong** — an empty resolved tenant id makes
  tenant-scoped tools fail rather than default; a *wrong* one means the partner record's
  tenant is wrong, which lives in `chora-a2a-gateway`.
- **Traces never reach Tempo** — every service in this stack logs
  `invalid target address http://otel-collector:4317 ... too many colons in address`.
  Observed in the running stack; only tracing is affected.

## API surface

No inbound HTTP other than these five routes. All three health endpoints accept `GET`.

| Method | Path | Purpose |
| --- | --- | --- |
| `GET` | `/health`, `/healthz`, `/readyz` | liveness and readiness; all return the same body |
| `POST` | `/mcp/tools/list` | JSON-RPC `tools/list`; requires `X-API-Key` |
| `POST` | `/mcp/tools/call` | JSON-RPC `tools/call`; requires `X-API-Key` |

`tools/list` request:

```json
{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}
```

Each returned tool carries `name`, `description`, `inputSchema` and `requiredScope`.

`tools/call` request:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {"name": "course_catalog", "arguments": {"limit": 10, "offset": 0}}
}
```

Both endpoints require the `X-API-Key` header and a JSON-RPC 2.0 body; the method field
must match the endpoint.

## Architecture

```text
cmd/server/            entrypoint and wiring
internal/              resolver, tool catalogue, JSON-RPC handling, upstream clients
Dockerfile             multi-arch image
```

There is no storage layer to reason about. The service is a pure function of the request
plus the resolver's answer: authenticate, look up the allowlist, dispatch to one of five
upstreams, translate the result back to JSON-RPC. That is why it can be rebuilt at any
time with no state loss and why its only failure modes are authentication and upstream
reachability.

It also propagates W3C trace context: an inbound `traceparent`/`tracestate` is forwarded to
the upstream call, and `X-Echoed-Traceparent` comes back when an inbound `traceparent` was
present. That echo is a convenient way to confirm a request actually traversed the gateway.

## Events

**None.** This service neither publishes nor consumes events: it has no NATS connection, no
outbox and no subscribers. Its entire interaction surface is synchronous HTTP — the inbound
JSON-RPC call and the outbound upstream request.

If a deployment tells you the MCP gateway is not consuming a topic, that is correct
behaviour rather than a fault.

## Migrations

**None.** There is no database, no `migrations/` directory and no entry in the stack's
migrate one-shot set. All state — partners, API keys, allowlists — lives in
`chora-a2a-gateway`.

If you are auditing a deployment and expect an `mcp-gateway-migrate` container, there isn't
one and there should not be.
