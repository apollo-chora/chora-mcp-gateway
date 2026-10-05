# chora-mcp-gateway

Model Context Protocol (MCP) gateway exposing Chora capabilities to external AI
agents (Claude Desktop, custom LLM-driven copilots) over JSON-RPC 2.0.

This is the **MCP-as-a-Service** deliverable referenced by CHO-27 and the
`domain-a2a` skill (MCP Gateway add-on). It complements `chora-a2a-gateway`:
A2A serves agent-to-agent invocations under JWS signatures; MCP serves the
JSON-RPC 2.0 protocol consumed by hosted LLM tools.

The service is standalone and provider-neutral. Shared Chora modules are
consumed as Go modules (`github.com/apollo-chora/chora-common`). There is no
database, no migrations, and no message-bus dependency; tracing uses OTLP
(`OTEL_EXPORTER_OTLP_ENDPOINT`).

## Surface

| Endpoint | Method | Auth | Purpose |
|---|---|---|---|
| `/health`, `/healthz`, `/readyz` | GET | none | Liveness / readiness |
| `/mcp/tools/list` | POST | `X-API-Key` | JSON-RPC 2.0 — tool catalogue scoped to partner |
| `/mcp/tools/call` | POST | `X-API-Key` | JSON-RPC 2.0 — execute a tool |

## Tools

Tool execution fans out to the owning Chora domain service over the HTTP-client
seam (`internal/adapter/clients`, `Dispatcher`). The partner's owning tenant is
propagated as `X-Tenant-Id`; results carry a `citations` array derived from the
**real** upstream entities (IMDA D4 Transparency) — never a stub.

| Name | Scope | Status | Upstream route |
|---|---|---|---|
| `course_catalog` | `mcp:read` | **wired** | `GET {MCP_UPSTREAM_DELIVERY}/api/courses` (tenant-only) |
| `learning_path_query` | `mcp:read` | **wired** | `GET {MCP_UPSTREAM_CONSUMPTION}/api/learning-paths` (tenant-only; optional `learner_gcid` filter) |
| `atom_search` | `mcp:read` | **fail-loud** | chora-creation gates `/api/atoms` on a learner `gcid` an AGID-only partner cannot supply |
| `governance_check` | `mcp:read` | **fail-loud** | chora-governance `/api/gatekeeper/evaluate` requires a `gcid` the partner cannot supply |
| `familiar_nudge` | `mcp:write` | **fail-loud** | chora-consumption exposes no HTTP nudge route |

Fail-loud tools return a `tools/call` result with `isError: true` and zero
citations — they never fabricate a success. `model_broker_invoke` was **removed**
(ADR-146 retired `chora-model-broker-router`; the chokepoint `chora-model-gateway`
is gRPC-only with no HTTP invoke route reachable over this seam).

## Identity model

External callers send a per-partner API key via `X-API-Key`. The gateway resolves
the key against the chora-a2a-gateway partner registry. When no gateway URL is
configured, a local-dev `internal/adapter/inmem.StaticResolver` can be seeded
from `MCP_DEV_SEED_API_KEYS`; an unconfigured process refuses to boot rather than
running with a silently-empty resolver.

The resolved `Identity` carries an **AGID** (UUIDv7) — distinct from GCID. The
`Identity` struct intentionally has no `Gcid` field; agents cannot hold
TenantMembership.

It also carries a **`TenantID`** — the tenant that purchased the per-tenant MCP
add-on and minted the API key (`POST /admin/mcp/{tenant_id}` in
chora-a2a-gateway). This is the *data scope* the partner acts within (not a GCID,
not an agent TenantMembership) and is propagated to upstream domain services as
`X-Tenant-Id`. An empty `TenantID` makes tenant-scoped tools fail loud.

## Requirements

- Go 1.26+
- Docker (optional, for the container image)

## Configuration

Copy `.env.example` to `.env` and fill in values. The `.env` file is ignored by
Git. There is no inline configuration; every upstream URL comes from the
environment.

| Env var | Purpose |
|---|---|
| `PORT` | HTTP listen port (default 8080) |
| `CHORA_ENV` | Deployment environment label |
| `CHORA_SOURCE_PROJECT` | Event/source project label (default `chora-local`) |
| `MCP_UPSTREAM_A2A_GATEWAY` | chora-a2a-gateway base URL (partner registry) |
| `MCP_UPSTREAM_DELIVERY` | chora-delivery base URL (course_catalog) |
| `MCP_UPSTREAM_CONSUMPTION` | chora-consumption base URL (learning_path_query) |
| `MCP_UPSTREAM_CREATION` | chora-creation base URL (atom_search — fail-loud today) |
| `MCP_UPSTREAM_GOVERNANCE` | chora-governance base URL (governance_check — fail-loud today) |
| `MCP_DEV_SEED_API_KEYS` | Dev-only `key:agid:tenant_id:tool1,tool2` seed (NEVER set in prod) |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC trace endpoint (the OTel Collector) |

(`MCP_UPSTREAM_MODEL_BROKER` was removed — ADR-146.)

## Run locally

```sh
PORT=8096 \
MCP_DEV_SEED_API_KEYS=dev-key:01900000-0000-7000-8000-000000000001:01900000-0000-7000-8000-0000000000aa:course_catalog,learning_path_query \
go run ./cmd/server
```

Then:

```sh
curl -s localhost:8096/readyz
```

## Tests

```sh
go test ./...
go test ./internal/domain/... -coverprofile=cover.out && go tool cover -func=cover.out | tail -1
```

## Observability

W3C `traceparent`/`tracestate` are read on inbound, echoed via
`X-Echoed-Traceparent`, and propagated onto every upstream fan-out request
(gateway→domain hop unbroken). Server-side spans are exported over OTLP/gRPC to
the endpoint in `OTEL_EXPORTER_OTLP_ENDPOINT` (stdout export when unset), using
GenAI/OpenInference span attributes.

## Container image

```sh
docker build -t chora-mcp-gateway .
```
