# chora-mcp-gateway

## About

chora-mcp-gateway is a Go HTTP service that exposes Chora capabilities to external clients through MCP-style JSON-RPC 2.0 endpoints. It authenticates callers with an `X-API-Key`, resolves the key through `chora-a2a-gateway` or a local development seed, and authorizes access by tool name. Tool calls are dispatched to Chora domain services over HTTP with the owning tenant propagated as `X-Tenant-Id`, and successful results include citations derived from upstream data.

## Quick start

### Prerequisites

- Go 1.26.1 or newer
- Docker only if you want to build the container image

### Run locally

The process requires either `MCP_UPSTREAM_A2A_GATEWAY` or the local-only `MCP_DEV_SEED_API_KEYS` setting. The example below uses the development resolver and starts the server on port 8096.

```sh
PORT=8096 \
MCP_DEV_SEED_API_KEYS=dev-key:01900000-0000-7000-8000-000000000001:01900000-0000-7000-8000-0000000000aa:course_catalog,learning_path_query \
go run ./cmd/server
```

In another shell, check the service:

```sh
curl -s localhost:8096/readyz
```

For an environment-based setup, copy `.env.example` to `.env` and export the values before starting the server. `.env` is ignored by Git.

## Usage

The service listens on `:8080` by default. Set `PORT` to change the HTTP port.

### Health endpoints

All three endpoints accept `GET`:

- `/health`
- `/healthz`
- `/readyz`

A successful response is:

```json
{"status":"ok","service":"chora-mcp-gateway"}
```

### MCP endpoints

Both MCP endpoints require the `X-API-Key` header and JSON-RPC 2.0 request bodies.

#### List tools

`POST /mcp/tools/list`

The request method must be `tools/list`. The response contains only tools present in the authenticated partner's allowed-tool list.

Example:

```sh
curl -s \
  -H 'X-API-Key: dev-key' \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' \
  http://localhost:8096/mcp/tools/list
```

Each published tool includes:

- `name`
- `description`
- `inputSchema`
- `requiredScope`

`requiredScope` is metadata only. Authorization is based on tool-name membership in the partner's allowlist.

#### Call a tool

`POST /mcp/tools/call`

Use a request of the form:

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "method": "tools/call",
  "params": {
    "name": "course_catalog",
    "arguments": {
      "limit": 10,
      "offset": 0
    }
  }
}
```

Example:

```sh
curl -s \
  -H 'X-API-Key: dev-key' \
  -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"course_catalog","arguments":{"limit":10,"offset":0}}}' \
  http://localhost:8096/mcp/tools/call
```

The current catalogue contains five tools:

| Tool | Scope metadata | Current behavior |
|---|---|---|
| `course_catalog` | `mcp:read` | Calls `GET {MCP_UPSTREAM_DELIVERY}/api/courses` |
| `learning_path_query` | `mcp:read` | Calls `GET {MCP_UPSTREAM_CONSUMPTION}/api/learning-paths` |
| `atom_search` | `mcp:read` | Fails loudly because the upstream route requires a learner GCID |
| `governance_check` | `mcp:read` | Fails loudly because the upstream route requires a GCID |
| `familiar_nudge` | `mcp:write` | Fails loudly because no upstream HTTP nudge route exists |

`course_catalog` supports optional numeric `limit` and `offset` arguments. `learning_path_query` supports an optional `learner_gcid` filter.

Failed tool execution is returned as a JSON-RPC result with `isError: true` and no citations. Unknown tools return JSON-RPC error code `-32002`. A caller without permission for a known tool receives HTTP 403 with code `TOOL_NOT_ALLOWED`.

### Authentication and tenant scope

Production deployments set `MCP_UPSTREAM_A2A_GATEWAY`. The gateway sends the caller's API key to:

```
POST {MCP_UPSTREAM_A2A_GATEWAY}/admin/mcp/_resolve
X-API-Key: <key>
```

The resolver response provides the partner AGID, tenant ID, tool allowlist, and active state. The authenticated tenant ID is propagated to upstream domain services as `X-Tenant-Id`. An empty tenant ID causes tenant-scoped tools to fail rather than using a default.

For local development, `MCP_DEV_SEED_API_KEYS` accepts:

```
key:agid:tenant_id:tool1,tool2
```

The seed is local-development only. If neither the production resolver URL nor a development seed is configured, the process refuses to start.

### Configuration

| Variable | Description |
|---|---|
| `PORT` | HTTP listen port, default `8080` |
| `CHORA_ENV` | Deployment environment label |
| `CHORA_SOURCE_PROJECT` | Source/event project label, default `chora-local` |
| `MCP_UPSTREAM_A2A_GATEWAY` | `chora-a2a-gateway` base URL used to resolve API keys |
| `MCP_UPSTREAM_DELIVERY` | `chora-delivery` base URL |
| `MCP_UPSTREAM_CONSUMPTION` | `chora-consumption` base URL |
| `MCP_UPSTREAM_CREATION` | `chora-creation` base URL |
| `MCP_UPSTREAM_GOVERNANCE` | `chora-governance` base URL |
| `MCP_DEV_SEED_API_KEYS` | Local-only API-key seed in `key:agid:tenant_id:tool1,tool2` format |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | OTLP/gRPC trace endpoint; unset falls back to stdout |

There is no `MCP_UPSTREAM_MODEL_BROKER` setting and no `model_broker_invoke` tool.

### Tracing

The service uses OpenTelemetry tracing. Incoming W3C `traceparent` and `tracestate` context is propagated to upstream HTTP calls, and `X-Echoed-Traceparent` is returned when an inbound `traceparent` header is present. Set `OTEL_EXPORTER_OTLP_ENDPOINT` to send traces to an OTLP/gRPC collector.

### Container

Build the image locally with:

```sh
docker build -t chora-mcp-gateway .
```

The repository's GitHub Actions workflow publishes multi-architecture images for `linux/amd64` and `linux/arm64` on pushes to `main` and version tags.

## Development

The project is a single Go module:

```text
.
├── cmd/server/              HTTP server entrypoint and bootstrap tests
├── internal/adapter/clients Upstream HTTP clients and tool dispatcher
├── internal/adapter/http    MCP and health handlers
├── internal/adapter/inmem   Local API-key resolver
├── internal/domain/auth     API-key authentication and identity
├── internal/domain/capability Tool-name authorization
├── internal/domain/tool      Tool catalogue and result types
├── internal/observability    OpenTelemetry setup and HTTP tracing middleware
├── .env.example              Example environment configuration
├── Dockerfile                Container build
├── go.mod
└── go.sum
```

Run the full test suite with:

```sh
go test ./...
```

Run domain tests with coverage:

```sh
go test ./internal/domain/... -coverprofile=cover.out
go tool cover -func=cover.out | tail -1
```

Build the server binary directly with:

```sh
go build ./cmd/server
```
