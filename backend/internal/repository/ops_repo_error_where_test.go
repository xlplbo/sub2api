package repository

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestBuildOpsErrorLogsWhere_QueryUsesQualifiedColumns(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		Query: "ACCESS_DENIED",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "e.request_id ILIKE $") {
		t.Fatalf("where should include qualified request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.client_request_id ILIKE $") {
		t.Fatalf("where should include qualified client_request_id condition: %s", where)
	}
	if !strings.Contains(where, "e.error_message ILIKE $") {
		t.Fatalf("where should include qualified error_message condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_UserQueryUsesExistsSubquery(t *testing.T) {
	filter := &service.OpsErrorLogFilter{
		UserQuery: "admin@",
	}

	where, args := buildOpsErrorLogsWhere(filter)
	if where == "" {
		t.Fatalf("where should not be empty")
	}
	if len(args) != 1 {
		t.Fatalf("args len = %d, want 1", len(args))
	}
	if !strings.Contains(where, "EXISTS (SELECT 1 FROM users u WHERE u.id = e.user_id AND u.email ILIKE $") {
		t.Fatalf("where should include EXISTS user email condition: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_ProxyIDMatchesAttemptAttribution(t *testing.T) {
	proxyID := int64(4)
	filter := &service.OpsErrorLogFilter{ProxyID: &proxyID, ProxyDirect: true}

	where, args := buildOpsErrorLogsWhere(filter)
	if len(args) != 1 || args[0] != proxyID {
		t.Fatalf("args = %v, want [4]", args)
	}
	if !strings.Contains(where, "e.upstream_errors @> jsonb_build_array(jsonb_build_object('proxy_id', $1::bigint))") {
		t.Fatalf("where should match proxy attribution on upstream attempts: %s", where)
	}
	if strings.Contains(where, "direct/no_proxy") {
		t.Fatalf("proxy id takes precedence over direct: %s", where)
	}
}

func TestBuildOpsErrorLogsWhere_ProxyDirectMatchesDirectAttempts(t *testing.T) {
	where, args := buildOpsErrorLogsWhere(&service.OpsErrorLogFilter{ProxyDirect: true})
	if len(args) != 0 {
		t.Fatalf("args len = %d, want 0", len(args))
	}
	if !strings.Contains(where, `e.upstream_errors @> '[{"proxy_name": "direct/no_proxy"}]'::jsonb`) {
		t.Fatalf("where should match direct attempts: %s", where)
	}
}
