package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/Wei-Shaw/sub2api/internal/testutil"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

type openAIWSAccountWaitSession struct {
	conn     *coderws.Conn
	cache    service.ConcurrencyCache
	requests chan []byte
	finished chan struct{}
	cancel   chan context.CancelCauseFunc
}

func TestOpenAIWSAccountWait_ExitReleasesResources(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		for _, reason := range []string{"timeout", "queue_full", "disconnect", "early_frame_disconnect"} {
			t.Run(mode+"/"+reason, func(t *testing.T) {
				f := newOpenAIWSAccountWaitSession(t, mode, 250*time.Millisecond)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				ok, err := f.cache.AcquireAccountSlot(ctx, 801, 1, "other")
				require.NoError(t, err)
				require.True(t, ok)
				if reason == "queue_full" {
					for range 2 {
						ok, err = f.cache.IncrementAccountWaitCount(ctx, 801, 2)
						require.NoError(t, err)
						require.True(t, ok)
					}
				}
				require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"blocked"}`)))
				if strings.Contains(reason, "disconnect") {
					require.Eventually(t, func() bool { n, _ := f.cache.GetAccountWaitingCount(ctx, 801); return n == 1 }, time.Second, time.Millisecond)
					if reason == "early_frame_disconnect" {
						require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"early"}`)))
					}
					_ = f.conn.CloseNow()
				} else {
					_, _, err = f.conn.Read(ctx)
					require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
				}
				select {
				case <-f.finished:
				case <-ctx.Done():
					t.Fatal("handler did not clean up")
				}
				waiting, err := f.cache.GetAccountWaitingCount(ctx, 801)
				require.NoError(t, err)
				if reason == "queue_full" {
					require.Equal(t, 2, waiting)
				} else {
					require.Zero(t, waiting)
				}
				accounts, err := f.cache.GetAccountConcurrency(ctx, 801)
				require.NoError(t, err)
				require.Equal(t, 1, accounts, "must preserve the other request's slot")
				users, err := f.cache.GetUserConcurrency(ctx, 1702)
				require.NoError(t, err)
				require.Zero(t, users)
				select {
				case <-f.requests:
					t.Fatal("canceled/expired request reached upstream")
				default:
				}
			})
		}
	}
}

func TestOpenAIWSAccountWaitBudget_Boundaries(t *testing.T) {
	b := &openAIWSAccountWaitBudget{turn: 1}
	require.True(t, b.canWait(service.OpenAIWSIngressModePassthrough))
	first := b.waitDeadline(time.Second, time.Time{})
	require.Equal(t, first, b.waitDeadline(time.Minute, time.Time{}), "retry must not reset the wait budget")
	retryDeadline := time.Now().Add(50 * time.Millisecond)
	require.Equal(t, retryDeadline, b.waitDeadline(time.Minute, retryDeadline))
	b.requestSent.Store(true)
	require.False(t, b.canWait(service.OpenAIWSIngressModePassthrough), "sent request cannot become a fresh passthrough request")
	b.nextTurn()
	require.True(t, b.deadline.IsZero())
	require.False(t, b.canWait(service.OpenAIWSIngressModePassthrough))
	require.True(t, b.canWait(service.OpenAIWSIngressModeCtxPool))
	require.True(t, b.canWait(service.OpenAIWSIngressModeHTTPBridge))
	require.False(t, b.canWait(service.OpenAIWSIngressModeOff))
	require.False(t, (&openAIWSAccountWaitBudget{continuation: true}).canWait(service.OpenAIWSIngressModePassthrough))
}

func TestOpenAIWSAccountWait_NoPlanAndAcquireCancellation(t *testing.T) {
	for _, scenario := range []string{"no_plan", "canceled_after_acquire", "canceled_after_wait_acquire", "cache_error"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			attempts := 0
			cache := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
				attempts++
				if scenario == "canceled_after_acquire" || (scenario == "canceled_after_wait_acquire" && attempts > 1) {
					cancel()
					return true, nil
				}
				if scenario == "cache_error" {
					return false, errors.New("redis unavailable")
				}
				return false, nil
			}}
			h := &OpenAIGatewayHandler{concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 0)}
			var plan *service.AccountWaitPlan
			if scenario == "canceled_after_wait_acquire" {
				plan = &service.AccountWaitPlan{Timeout: time.Second, MaxWaiting: 2}
			}
			release, err := h.acquireWSAccountSlot(ctx, &service.Account{ID: 801, Platform: service.PlatformOpenAI}, 1, plan, &openAIWSAccountWaitBudget{turn: 1}, service.OpenAIWSIngressModeCtxPool, "initial", time.Time{}, zap.NewNop())
			require.Nil(t, release)
			require.Error(t, err)
			if strings.HasPrefix(scenario, "canceled_after_") {
				require.ErrorIs(t, err, context.Canceled)
				require.EqualValues(t, 1, cache.releaseAccountCalled)
			} else {
				var closeErr *service.OpenAIWSClientCloseError
				require.ErrorAs(t, err, &closeErr)
				if scenario == "cache_error" {
					require.Equal(t, coderws.StatusInternalError, closeErr.StatusCode())
				} else {
					require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
				}
			}
		})
	}
}

func TestOpenAIWSAccountWait_ExpiredBudgetDoesNotReacquire(t *testing.T) {
	for _, source := range []string{"account_wait", "upstream_retry"} {
		t.Run(source, func(t *testing.T) {
			cache := &concurrencyCacheMock{acquireAccountSlotFn: func(context.Context, int64, int, string) (bool, error) {
				t.Error("expired recovery must not reacquire a slot")
				return true, nil
			}}
			h := &OpenAIGatewayHandler{concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, 0)}
			budget := &openAIWSAccountWaitBudget{turn: 1}
			var retryDeadline time.Time
			if source == "account_wait" {
				budget.deadline = time.Now().Add(-time.Second)
			} else {
				retryDeadline = time.Now().Add(-time.Second)
			}
			release, err := h.acquireWSAccountSlot(context.Background(), &service.Account{ID: 801, Platform: service.PlatformOpenAI}, 1,
				&service.AccountWaitPlan{Timeout: time.Second, MaxWaiting: 2}, budget, service.OpenAIWSIngressModeCtxPool, "retry", retryDeadline, zap.NewNop())
			if release != nil {
				release()
			}
			require.Nil(t, release)
			var closeErr *service.OpenAIWSClientCloseError
			require.ErrorAs(t, err, &closeErr)
			require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
		})
	}
}

func TestOpenAIWSAccountWait_LeaseLossReleasesResources(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		for _, later := range []bool{false, true} {
			if later && mode == service.OpenAIWSIngressModePassthrough {
				continue
			}
			t.Run(fmt.Sprintf("%s/later=%v", mode, later), func(t *testing.T) {
				f := newOpenAIWSAccountWaitSession(t, mode, 2*time.Second)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if later {
					require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first"}`)))
					_, _, err := f.conn.Read(ctx)
					require.NoError(t, err)
					<-f.requests
					require.Eventually(t, func() bool { n, _ := f.cache.GetAccountConcurrency(ctx, 801); return n == 0 }, time.Second, time.Millisecond)
				}
				ok, err := f.cache.AcquireAccountSlot(ctx, 801, 1, "other")
				require.NoError(t, err)
				require.True(t, ok)
				require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"waiting"}`)))
				require.Eventually(t, func() bool { n, _ := f.cache.GetAccountWaitingCount(ctx, 801); return n == 1 }, time.Second, time.Millisecond)
				(<-f.cancel)(service.ErrOpenAIWSIngressLeaseLost)
				_, _, err = f.conn.Read(ctx)
				require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
				require.Contains(t, err.Error(), "ingress capacity lease lost")
				select {
				case <-f.finished:
				case <-ctx.Done():
					t.Fatal("handler did not clean up")
				}
				n, err := f.cache.GetAccountWaitingCount(ctx, 801)
				require.NoError(t, err)
				require.Zero(t, n)
				n, err = f.cache.GetUserConcurrency(ctx, 1702)
				require.NoError(t, err)
				require.Zero(t, n)
				n, err = f.cache.GetAccountConcurrency(ctx, 801)
				require.NoError(t, err)
				require.Equal(t, 1, n)
				require.Empty(t, f.requests, "canceled turn must not reach upstream")
			})
		}
	}
}

func newOpenAIWSAccountWaitSession(t *testing.T, mode string, timeout time.Duration) *openAIWSAccountWaitSession {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := &openAIWSAccountWaitSession{requests: make(chan []byte, 8), finished: make(chan struct{}), cancel: make(chan context.CancelCauseFunc, 1)}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			body, _ := io.ReadAll(r.Body)
			f.requests <- body
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, "data: "+accountWaitCompleted+"\n\n")
			return
		}
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			_, body, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			f.requests <- body
			if conn.Write(r.Context(), coderws.MessageText, []byte(accountWaitCompleted)) != nil {
				return
			}
		}
	}))
	t.Cleanup(upstream.Close)
	repo := &openAIWSTurnBudgetAccountRepo{accounts: []service.Account{{
		ID: 801, Name: "account-wait", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test", "base_url": upstream.URL},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": mode},
	}}}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 2
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 2
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.Scheduling.StickySessionWaitTimeout = timeout
	cfg.Gateway.Scheduling.FallbackWaitTimeout = timeout
	cfg.Gateway.Scheduling.StickySessionMaxWaiting = 2
	cfg.Gateway.Scheduling.FallbackMaxWaiting = 2
	f.cache = testutil.NewTestConcurrencyCache(t)
	concurrency := service.NewConcurrencyService(f.cache)
	billing := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billing.Stop)
	gateway := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, concurrency,
		service.NewBillingService(cfg, nil), nil, billing, openAIWSTurnBudgetHTTPClient{}, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	t.Cleanup(gateway.CloseOpenAIWSPool)
	h := NewOpenAIGatewayHandler(gateway, concurrency, billing, &service.APIKeyService{}, nil, nil, nil, nil, cfg)
	groupID := int64(4202)
	apiKey := &service.APIKey{ID: 1802, GroupID: &groupID, User: &service.User{ID: 1702, Status: service.StatusActive}, Group: &service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive}}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1702, Concurrency: 1})
		c.Next()
	})
	router.GET("/v1/responses", func(c *gin.Context) {
		defer close(f.finished)
		ctx, cancel := context.WithCancelCause(c.Request.Context())
		defer cancel(nil)
		f.cancel <- cancel
		c.Request = c.Request.WithContext(ctx)
		h.ResponsesWebSocket(c)
	})
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var err error
	f.conn, _, err = coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http")+"/v1/responses", nil)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = f.conn.CloseNow()
		select {
		case <-f.finished:
		case <-time.After(3 * time.Second):
			t.Error("handler did not exit")
		}
	})
	return f
}

const accountWaitCompleted = `{"type":"response.completed","response":{"id":"resp_wait","model":"gpt-5.1","output":[],"usage":{"input_tokens":1,"output_tokens":1}}}`

func TestOpenAIWSAccountWait_ReleaseContinuesSameConnection(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModeCtxPool, service.OpenAIWSIngressModeHTTPBridge, service.OpenAIWSIngressModePassthrough} {
		for _, later := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/later=%v", mode, later), func(t *testing.T) {
				f := newOpenAIWSAccountWaitSession(t, mode, 2*time.Second)
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if later {
					require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"first"}`)))
					_, _, err := f.conn.Read(ctx)
					require.NoError(t, err)
					<-f.requests
					require.Eventually(t, func() bool { n, _ := f.cache.GetAccountConcurrency(ctx, 801); return n == 0 }, time.Second, time.Millisecond)
				}
				acquired, err := f.cache.AcquireAccountSlot(ctx, 801, 1, "other-request")
				require.NoError(t, err)
				require.True(t, acquired)
				require.NoError(t, f.conn.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","input":"waiting"}`)))
				if later && mode == service.OpenAIWSIngressModePassthrough {
					_, _, err = f.conn.Read(ctx)
					require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
					return
				}
				require.Eventually(t, func() bool { n, _ := f.cache.GetAccountWaitingCount(ctx, 801); return n == 1 }, time.Second, time.Millisecond, "request should wait without closing")
				select {
				case <-f.requests:
					t.Fatal("upstream called before account admission")
				default:
				}
				require.NoError(t, f.cache.ReleaseAccountSlot(ctx, 801, "other-request"))
				_, payload, err := f.conn.Read(ctx)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(payload, "type").String())
				select {
				case request := <-f.requests:
					require.Contains(t, string(request), "waiting")
				case <-ctx.Done():
					t.Fatal("upstream request missing")
				}
				require.Eventually(t, func() bool { n, _ := f.cache.GetAccountWaitingCount(ctx, 801); return n == 0 }, time.Second, time.Millisecond)
			})
		}
	}
}
