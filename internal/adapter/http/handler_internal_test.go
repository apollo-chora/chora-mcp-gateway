// handler_internal_test.go — in-package (white-box) tests for unexported
// helpers that are not reachable through the public HTTP surface. Every
// production call site passes the jsonrpc version explicitly, so writeRPC's
// default-fill guard only fires via a direct call — which external-package
// tests cannot make.
package httpadapter

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteRPCDefaultsJSONRPC(t *testing.T) {
	t.Parallel()
	rec := httptest.NewRecorder()
	writeRPC(rec, http.StatusOK, jsonRPCResponse{Result: map[string]string{"ok": "1"}})
	body := rec.Body.String()
	if !strings.Contains(body, `"jsonrpc":"2.0"`) {
		t.Errorf("expected default jsonrpc version in envelope, got %s", body)
	}
	if !strings.Contains(body, `"ok":"1"`) {
		t.Errorf("expected result to survive the default-fill, got %s", body)
	}
}
