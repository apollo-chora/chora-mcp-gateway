// Package httpadapter wires MCP JSON-RPC 2.0 endpoints for
// chora-mcp-gateway:
//
//	GET  /health                  → liveness
//	GET  /readyz                  → readiness
//	POST /mcp/tools/list          → MCP tool discovery (JSON-RPC 2.0)
//	POST /mcp/tools/call          → MCP tool invocation (JSON-RPC 2.0)
//
// Auth: every /mcp/* call requires the `X-API-Key` header. The key is
// resolved against an auth.Resolver (in M11 backed by the in-memory
// StaticResolver; M12 will swap in a gRPC client to
// chora-a2a-gateway/partners).
//
// Capability gating: a partner may call a tool iff the tool's NAME is
// present in the partner's allowed-tool list (auth.Identity.AllowedTools,
// wire field `allowed_scopes`). Partners whose allowlist omits a tool's
// name receive HTTP 403 with code `TOOL_NOT_ALLOWED` for tools/call, and
// tools/list returns only the catalogue ∩ allowlist intersection.
// RequiredScope (`mcp:read`/`mcp:write`) is informational metadata only
// — it is NOT consulted for authorization.
//
// Citations: every successful `tools/call` response carries at least
// one Citation (IMDA D4 Transparency dimension), derived from the real
// upstream entities returned by the clients.Dispatcher fan-out.
package httpadapter

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/apollo-chora/chora-mcp-gateway/internal/adapter/clients"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/auth"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/capability"
	"github.com/apollo-chora/chora-mcp-gateway/internal/domain/tool"
)

// JSON-RPC 2.0 error codes.
const (
	rpcParseError     = -32700
	rpcInvalidRequest = -32600
	rpcMethodNotFound = -32601
	rpcInvalidParams  = -32602
	rpcInternalError  = -32603

	// MCP-specific app codes (per JSON-RPC 2.0 server error range).
	rpcAppToolNotAllowed = -32001
	rpcAppUnknownTool    = -32002
)

// Router multiplexes the MCP HTTP endpoints.
type Router struct {
	mux           *http.ServeMux
	authenticator *auth.Authenticator
	gate          *capability.Gate
	catalog       tool.Catalog
	dispatcher    *clients.Dispatcher
}

// NewRouter wires the MCP gateway router. The caller passes any object
// that implements auth.Resolver (in-memory or remote partner registry)
// plus the upstream clients.Registry the tool dispatcher fans out over.
func NewRouter(resolver auth.Resolver, registry *clients.Registry) *Router {
	r := &Router{
		mux:           http.NewServeMux(),
		authenticator: auth.NewAuthenticator(resolver),
		gate:          capability.NewGate(),
		catalog:       tool.DefaultCatalog(),
		dispatcher:    clients.NewDispatcher(registry),
	}
	r.routes()
	return r
}

// ServeHTTP implements http.Handler.
func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) routes() {
	r.mux.HandleFunc("/health", r.health)
	r.mux.HandleFunc("/healthz", r.health)
	r.mux.HandleFunc("/readyz", r.health)
	r.mux.HandleFunc("/mcp/tools/list", r.toolsList)
	r.mux.HandleFunc("/mcp/tools/call", r.toolsCall)
}

// -----------------------------------------------------------------------------
// Health
// -----------------------------------------------------------------------------

func (r *Router) health(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "chora-mcp-gateway",
	})
}

// -----------------------------------------------------------------------------
// JSON-RPC envelope
// -----------------------------------------------------------------------------

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type jsonRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *jsonRPCError   `json:"error,omitempty"`
}

// -----------------------------------------------------------------------------
// /mcp/tools/list
// -----------------------------------------------------------------------------

type toolDefDTO struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	InputSchema   map[string]any `json:"inputSchema"`
	RequiredScope string         `json:"requiredScope"`
}

func (r *Router) toolsList(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := r.authenticate(w, req)
	if !ok {
		return
	}
	rpc, parseErr := decodeRPC(req)
	if parseErr != nil {
		writeRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: rpcParseError, Message: parseErr.Error()},
		})
		return
	}
	if rpc.Method != "tools/list" {
		writeRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0", ID: rpc.ID,
			Error: &jsonRPCError{Code: rpcMethodNotFound, Message: "expected method=tools/list, got " + rpc.Method},
		})
		return
	}
	defs := r.catalog.FilterByAllowedNames(id.AllowedTools)
	dtos := make([]toolDefDTO, 0, len(defs))
	for _, d := range defs {
		dtos = append(dtos, toolDefDTO{
			Name:          d.Name,
			Description:   d.Description,
			InputSchema:   d.InputSchema,
			RequiredScope: d.RequiredScope,
		})
	}
	writeRPC(w, http.StatusOK, jsonRPCResponse{
		JSONRPC: "2.0", ID: rpc.ID,
		Result: map[string]any{"tools": dtos},
	})
}

// -----------------------------------------------------------------------------
// /mcp/tools/call
// -----------------------------------------------------------------------------

type callParams struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments"`
}

type resultDTO struct {
	Content   []tool.ContentBlock `json:"content"`
	Citations []tool.Citation     `json:"citations"`
	IsError   bool                `json:"isError"`
}

func (r *Router) toolsCall(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	id, ok := r.authenticate(w, req)
	if !ok {
		return
	}
	rpc, parseErr := decodeRPC(req)
	if parseErr != nil {
		writeRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0",
			Error:   &jsonRPCError{Code: rpcParseError, Message: parseErr.Error()},
		})
		return
	}
	if rpc.Method != "tools/call" {
		writeRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0", ID: rpc.ID,
			Error: &jsonRPCError{Code: rpcMethodNotFound, Message: "expected method=tools/call, got " + rpc.Method},
		})
		return
	}
	var params callParams
	if len(rpc.Params) > 0 {
		if err := json.Unmarshal(rpc.Params, &params); err != nil {
			writeRPC(w, http.StatusOK, jsonRPCResponse{
				JSONRPC: "2.0", ID: rpc.ID,
				Error: &jsonRPCError{Code: rpcInvalidParams, Message: err.Error()},
			})
			return
		}
	}
	def, found := r.catalog.Get(params.Name)
	if !found {
		writeRPC(w, http.StatusOK, jsonRPCResponse{
			JSONRPC: "2.0", ID: rpc.ID,
			Error: &jsonRPCError{
				Code:    rpcAppUnknownTool,
				Message: "unknown tool: " + params.Name,
			},
		})
		return
	}
	if err := r.gate.Authorize(def.Name, id.AllowedTools); err != nil {
		// HTTP 403 maps to OpenAPI Forbidden response.
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": err.Error(),
			"code":  "TOOL_NOT_ALLOWED",
		})
		return
	}
	// Real upstream fan-out (clients.Dispatcher). The partner's owning
	// tenant + trace context ride along so upstreams scope correctly and
	// the W3C trace is unbroken across the gateway→upstream hop.
	ctx := clients.WithTrace(req.Context(),
		req.Header.Get("traceparent"), req.Header.Get("tracestate"))
	res := r.dispatcher.Execute(ctx, def.Name, params.Arguments, id)
	writeRPC(w, http.StatusOK, jsonRPCResponse{
		JSONRPC: "2.0", ID: rpc.ID,
		Result: resultDTO{
			Content:   res.Content,
			Citations: res.Citations,
			IsError:   res.IsError,
		},
	})
}

// -----------------------------------------------------------------------------
// Helpers
// -----------------------------------------------------------------------------

func (r *Router) authenticate(w http.ResponseWriter, req *http.Request) (auth.Identity, bool) {
	key := req.Header.Get("X-API-Key")
	id, err := r.authenticator.Authenticate(key)
	if err == nil {
		return id, true
	}
	switch {
	case errors.Is(err, auth.ErrMissingKey):
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": err.Error(), "code": "MISSING_API_KEY",
		})
	case errors.Is(err, auth.ErrInvalidKey):
		writeJSON(w, http.StatusUnauthorized, map[string]string{
			"error": err.Error(), "code": "INVALID_API_KEY",
		})
	case errors.Is(err, auth.ErrPartnerInactive):
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": err.Error(), "code": "PARTNER_INACTIVE",
		})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error": err.Error(), "code": "INTERNAL",
		})
	}
	return auth.Identity{}, false
}

func decodeRPC(req *http.Request) (jsonRPCRequest, error) {
	var rpc jsonRPCRequest
	if err := json.NewDecoder(req.Body).Decode(&rpc); err != nil {
		return jsonRPCRequest{}, err
	}
	if rpc.JSONRPC != "2.0" {
		return jsonRPCRequest{}, errors.New("invalid jsonrpc envelope (need 2.0)")
	}
	return rpc, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("encode error: %v", err)
	}
}

func writeRPC(w http.ResponseWriter, status int, resp jsonRPCResponse) {
	if resp.JSONRPC == "" {
		resp.JSONRPC = "2.0"
	}
	writeJSON(w, status, resp)
}
