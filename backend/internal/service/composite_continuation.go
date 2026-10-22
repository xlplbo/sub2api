package service

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"time"
)

type CompositeRouteBinding struct {
	TargetPlatform string `json:"target_platform"`
	UpstreamModel  string `json:"upstream_model"`
}

func (b *CompositeRouteBinding) matches(decision CompositeRouteDecision) bool {
	return b.TargetPlatform == decision.TargetPlatform && b.UpstreamModel == decision.UpstreamModel
}

type CompositeContinuationCache interface {
	Get(ctx context.Context, key string) (*CompositeRouteBinding, error)
	Set(ctx context.Context, key string, binding CompositeRouteBinding, ttl time.Duration) error
}

type compositeRouteUserKey struct{}

func WithCompositeRouteUser(ctx context.Context, userID int64) context.Context {
	return context.WithValue(ctx, compositeRouteUserKey{}, userID)
}

func (r *CompositeRouteResolver) SetContinuationCache(cache CompositeContinuationCache) {
	r.continuations = cache
}

func compositeContinuationKey(ctx context.Context, groupID int64, model, kind, id string) string {
	userID, _ := ctx.Value(compositeRouteUserKey{}).(int64)
	id = strings.TrimSpace(id)
	if userID <= 0 || groupID <= 0 || id == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%q:%q:%q", userID, groupID, model, kind, id)))
	return fmt.Sprintf("composite:continuation:%x", digest)
}

func (r *CompositeRouteResolver) continuation(ctx context.Context, groupID int64, model, kind, id string) (*CompositeRouteBinding, error) {
	if r == nil || r.continuations == nil {
		return nil, nil
	}
	key := compositeContinuationKey(ctx, groupID, model, kind, id)
	if key == "" {
		return nil, nil
	}
	cacheCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	return r.continuations.Get(cacheCtx, key)
}

func (r *CompositeRouteResolver) RememberResponse(ctx context.Context, decision CompositeRouteDecision, responseID, conversationID string) error {
	if r == nil || r.continuations == nil || !decision.TrackContinuation {
		return nil
	}
	cacheCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	for _, reference := range []struct{ kind, id string }{{"response", responseID}, {"conversation", conversationID}} {
		key := compositeContinuationKey(ctx, decision.GroupID, decision.PublicModel, reference.kind, reference.id)
		if key == "" {
			continue
		}
		if err := r.continuations.Set(cacheCtx, key, CompositeRouteBinding{
			TargetPlatform: decision.TargetPlatform, UpstreamModel: decision.UpstreamModel,
		}, time.Hour); err != nil {
			return err
		}
	}
	return nil
}
