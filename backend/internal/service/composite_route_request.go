package service

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/requestimage"
	"github.com/tidwall/gjson"
)

var ErrCompositeRouteHistoryRequired = errors.New("image-dependent routing requires full history; resend without previous_response_id or conversation")

func (r *CompositeRouteResolver) ResolveRequest(ctx context.Context, groupID int64, model, endpoint string, body []byte) (CompositeRouteDecision, error) {
	hasImage := requestimage.HasImage(body)
	model = strings.TrimSpace(model)
	endpoint = normalizeCompositeRouteEndpoint(endpoint)
	routes, err := r.requestRoutes(ctx, groupID, model)
	if err != nil {
		return CompositeRouteDecision{}, err
	}
	decision, err := r.resolveWithRoutes(ctx, groupID, model, endpoint, routes, hasImage)
	if err != nil || endpoint != CompositeRouteEndpointResponses {
		return decision, err
	}
	imageRoute, matched := matchCompositeRouteWithImage(routes, model, endpoint, true)
	alternative := decision
	if matched && imageRoute.RequestCondition == CompositeRouteConditionHasImage {
		alternative, err = r.resolveWithRoutes(ctx, groupID, model, endpoint, routes, !hasImage)
		if err != nil {
			return decision, err
		}
	}
	decision.TrackContinuation = !sameCompositeDestination(decision, alternative)
	alternative.TrackContinuation = decision.TrackContinuation
	references := []struct{ kind, id string }{
		{"response", gjson.GetBytes(body, "previous_response_id").String()},
		{"conversation", compositeConversationID(gjson.GetBytes(body, "conversation"))},
	}
	chosen := decision
	haveBinding := false
	for _, reference := range references {
		if strings.TrimSpace(reference.id) == "" {
			continue
		}
		binding, err := r.continuation(ctx, groupID, model, reference.kind, reference.id)
		if err != nil {
			return decision, fmt.Errorf("get composite continuation: %w", err)
		}
		if binding == nil {
			if decision.TrackContinuation {
				return decision, ErrCompositeRouteHistoryRequired
			}
			continue
		}
		target := decision
		if !binding.matches(target) {
			if hasImage || !binding.matches(alternative) {
				return decision, ErrCompositeRouteHistoryRequired
			}
			target = alternative
		}
		if haveBinding && !sameCompositeDestination(chosen, target) {
			return decision, ErrCompositeRouteHistoryRequired
		}
		target.TrackContinuation = true
		chosen, haveBinding = target, true
	}
	return chosen, nil
}

func sameCompositeDestination(a, b CompositeRouteDecision) bool {
	return a.TargetPlatform == b.TargetPlatform && a.UpstreamModel == b.UpstreamModel
}

func compositeConversationID(value gjson.Result) string {
	if value.IsObject() {
		value = value.Get("id")
	}
	if value.Type != gjson.String {
		return ""
	}
	return value.String()
}
