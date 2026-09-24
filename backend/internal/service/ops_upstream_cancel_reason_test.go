//go:build unit

package service

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// Proxy health counts request_error attempts as proxy failures unless they carry
// reason=request_canceled, so every site that records one must decide the reason.
func TestOpsUpstreamRequestErrorEventsSetReason(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)

	fset := token.NewFileSet()
	checked := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		require.NoError(t, err)
		ast.Inspect(parsed, func(n ast.Node) bool {
			lit, ok := n.(*ast.CompositeLit)
			if !ok {
				return true
			}
			if ident, ok := lit.Type.(*ast.Ident); !ok || ident.Name != "OpsUpstreamErrorEvent" {
				return true
			}
			kind, hasReason := "", false
			for _, elt := range lit.Elts {
				kv, ok := elt.(*ast.KeyValueExpr)
				if !ok {
					continue
				}
				key, ok := kv.Key.(*ast.Ident)
				if !ok {
					continue
				}
				switch key.Name {
				case "Kind":
					if basic, ok := kv.Value.(*ast.BasicLit); ok && basic.Kind == token.STRING {
						kind, _ = strconv.Unquote(basic.Value)
					}
				case "Reason":
					hasReason = true
				}
			}
			if strings.HasSuffix(kind, "request_error") {
				checked++
				require.Truef(t, hasReason, "%s: %s event must set Reason (opsUpstreamTransportReason)", fset.Position(lit.Pos()), kind)
			}
			return true
		})
	}
	require.Greater(t, checked, 5, "the guard must find the request_error sites")
}

func TestSanitizeOpsUpstreamErrorsKeepsCancelReason(t *testing.T) {
	events := make([]*OpsUpstreamErrorEvent, 0, opsUpstreamErrorsBodyWindow+2)
	proxyID := int64(4)
	events = append(events, &OpsUpstreamErrorEvent{
		ProxyID: &proxyID, ProxyName: "蜗牛", Kind: "request_error",
		Reason: opsUpstreamReasonRequestCanceled, Message: "context canceled",
	})
	for i := 0; i < opsUpstreamErrorsBodyWindow+1; i++ {
		events = append(events, &OpsUpstreamErrorEvent{
			ProxyID: &proxyID, ProxyName: "蜗牛", Kind: "request_error", Message: "socks connect",
		})
	}
	entry := &OpsInsertErrorLogInput{UpstreamErrors: events}

	require.NoError(t, sanitizeOpsUpstreamErrors(entry))
	require.NotNil(t, entry.UpstreamErrorsJSON)

	var stored []map[string]any
	require.NoError(t, json.Unmarshal([]byte(*entry.UpstreamErrorsJSON), &stored))
	require.Len(t, stored, len(events))
	require.Equal(t, opsUpstreamReasonRequestCanceled, stored[0]["reason"], "older attempts outside the body window keep the reason")
	require.NotContains(t, stored[1], "reason")
}
