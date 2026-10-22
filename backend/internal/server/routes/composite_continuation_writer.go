package routes

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const compositeContinuationBufferLimit = 1 << 20

type compositeContinuationWriter struct {
	gin.ResponseWriter
	ctx                        context.Context
	resolver                   *service.CompositeRouteResolver
	decision                   service.CompositeRouteDecision
	pending                    []byte
	dropping                   bool
	finished                   bool
	responseID, conversationID string
}

func newCompositeContinuationWriter(writer gin.ResponseWriter, ctx context.Context, resolver *service.CompositeRouteResolver, decision service.CompositeRouteDecision) *compositeContinuationWriter {
	return &compositeContinuationWriter{ResponseWriter: writer, ctx: ctx, resolver: resolver, decision: decision}
}

func (w *compositeContinuationWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

func (w *compositeContinuationWriter) Write(data []byte) (int, error) {
	w.observe(data)
	return w.ResponseWriter.Write(data)
}

func (w *compositeContinuationWriter) WriteString(data string) (int, error) {
	return w.Write([]byte(data))
}

func (w *compositeContinuationWriter) observe(data []byte) {
	if w.finished || w.Status() < 200 || w.Status() >= 300 {
		return
	}
	if !strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		if len(w.pending) == 0 && gjson.ValidBytes(data) {
			w.remember(gjson.ParseBytes(data))
			w.finished = true
			return
		}
		if len(w.pending)+len(data) > compositeContinuationBufferLimit {
			w.finished, w.pending = true, nil
			return
		}
		w.pending = append(w.pending, data...)
		if gjson.ValidBytes(w.pending) {
			w.remember(gjson.ParseBytes(w.pending))
			w.finished, w.pending = true, nil
		}
		return
	}
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		fragment := data
		if end >= 0 {
			fragment = data[:end]
		}
		if !w.dropping && len(w.pending)+len(fragment) <= compositeContinuationBufferLimit {
			w.pending = append(w.pending, fragment...)
		} else {
			w.dropping, w.pending = true, nil
		}
		if end < 0 {
			return
		}
		if !w.dropping && bytes.HasPrefix(w.pending, []byte("data:")) {
			event := gjson.ParseBytes(bytes.TrimSpace(w.pending[5:]))
			switch event.Get("type").String() {
			case "response.created", "response.completed", "response.done", "response.incomplete":
				w.remember(event.Get("response"))
			}
		}
		w.pending, w.dropping = w.pending[:0], false
		data = data[end+1:]
	}
}

func (w *compositeContinuationWriter) remember(response gjson.Result) {
	if !response.IsObject() {
		return
	}
	id := response.Get("id").String()
	if id == "" || response.Get("object").String() != "response" {
		return
	}
	conversation := response.Get("conversation")
	if conversation.IsObject() {
		conversation = conversation.Get("id")
	}
	conversationID := conversation.String()
	newID, newConversation := id, conversationID
	if id == w.responseID {
		newID = ""
	}
	if conversationID == w.conversationID {
		newConversation = ""
	}
	if newID == "" && newConversation == "" {
		return
	}
	if err := w.resolver.RememberResponse(w.ctx, w.decision, newID, newConversation); err != nil {
		logger.L().Warn("composite.continuation_bind_failed", zap.Error(err))
		return
	}
	w.responseID, w.conversationID = id, conversationID
}
