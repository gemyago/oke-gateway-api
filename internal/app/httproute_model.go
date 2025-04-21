package app

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/samber/lo"
	"go.uber.org/dig"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

type resolvedRouteDetails struct {
	gatewayDetails acceptedGatewayDetails
	matchedRef     gatewayv1.ParentReference
	httpRoute      gatewayv1.HTTPRoute
}

// httpRouteModel defines the interface for managing HTTPRoute resources.
type httpRouteModel interface {
	// resolveRequest resolves the parent details for a given HTTPRoute.
	// It returns true if the request is relevant for this controller and
	// the parent has been resolved.
	resolveRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *resolvedRouteDetails,
	) (bool, error)

	// acceptRoute accepts a reconcile request for a given HTTPRoute.
	acceptRoute(
		ctx context.Context,
		routeDetails *resolvedRouteDetails,
	) error
}

type httpRouteModelImpl struct {
	client       k8sClient
	logger       *slog.Logger
	gatewayModel gatewayModel
}

func (m *httpRouteModelImpl) resolveRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *resolvedRouteDetails,
) (bool, error) {
	var httpRoute gatewayv1.HTTPRoute
	if err := m.client.Get(ctx, req.NamespacedName, &httpRoute); err != nil {
		return false, err
	}

	var resolvedGatewayData acceptedGatewayDetails
	var matchedRef gatewayv1.ParentReference
	gatewayAccepted := false
	for _, parentRef := range httpRoute.Spec.ParentRefs {
		gatewayNamespace := req.NamespacedName.Namespace
		if parentRef.Namespace != nil {
			gatewayNamespace = string(lo.FromPtr(parentRef.Namespace))
		}
		parentName := types.NamespacedName{
			Namespace: gatewayNamespace,
			Name:      string(parentRef.Name),
		}
		m.logger.DebugContext(ctx, "Resolving parent for HTTProute",
			slog.String("parentName", parentName.String()),
			slog.Any("parentRef", parentRef),
		)
		accepted, err := m.gatewayModel.acceptReconcileRequest(ctx, reconcile.Request{
			NamespacedName: parentName,
		}, &resolvedGatewayData)
		if err != nil {
			return false, fmt.Errorf("failed to accept reconcile request for gateway %s: %w", parentRef.Name, err)
		}
		if accepted {
			gatewayAccepted = true
			matchedRef = parentRef
			break
		}
	}

	if !gatewayAccepted {
		m.logger.InfoContext(ctx, "No relevant gateway found for HTTProute",
			slog.String("route", req.NamespacedName.String()),
			slog.Int("triedParentRefs", len(httpRoute.Spec.ParentRefs)),
		)
		return false, nil
	}

	m.logger.InfoContext(ctx, "Resolved relevant HTTProute parent",
		slog.String("route", req.NamespacedName.String()),
		slog.String("gateway", resolvedGatewayData.gateway.Name),
	)

	receiver.httpRoute = httpRoute
	receiver.matchedRef = matchedRef
	receiver.gatewayDetails = resolvedGatewayData

	return true, nil
}

func (m *httpRouteModelImpl) acceptRoute(
	ctx context.Context,
	routeDetails *resolvedRouteDetails,
) error {
	parentStatus, parentStatusIndex, found := lo.FindIndexOf(
		routeDetails.httpRoute.Status.Parents,
		func(s gatewayv1.RouteParentStatus) bool {
			return s.ControllerName == routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName
		})
	if found {
		existingCondition := meta.FindStatusCondition(
			parentStatus.Conditions,
			string(gatewayv1.RouteConditionAccepted),
		)
		if existingCondition != nil &&
			existingCondition.ObservedGeneration == routeDetails.httpRoute.Generation &&
			existingCondition.Status == metav1.ConditionTrue {
			m.logger.DebugContext(ctx, "HTTProute already accepted",
				slog.String("route", routeDetails.httpRoute.Name),
				slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
			)
			return nil
		}
	} else {
		parentStatus = gatewayv1.RouteParentStatus{
			ParentRef:      routeDetails.matchedRef,
			ControllerName: routeDetails.gatewayDetails.gatewayClass.Spec.ControllerName,
		}
	}
	meta.SetStatusCondition(&parentStatus.Conditions, metav1.Condition{
		Type:               string(gatewayv1.RouteConditionAccepted),
		Status:             metav1.ConditionTrue,
		Reason:             string(gatewayv1.RouteReasonAccepted),
		ObservedGeneration: routeDetails.httpRoute.Generation,
		LastTransitionTime: metav1.Now(),
		Message:            fmt.Sprintf("Route accepted by %s", routeDetails.gatewayDetails.gateway.Name),
	})

	if found {
		m.logger.InfoContext(ctx, "Updating HTTProute status as Accepted",
			slog.String("route", routeDetails.httpRoute.Name),
			slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
		)
		routeDetails.httpRoute.Status.Parents[parentStatusIndex] = parentStatus
	} else {
		m.logger.InfoContext(ctx, "Accepting new HTTProute",
			slog.String("route", routeDetails.httpRoute.Name),
			slog.String("gateway", routeDetails.gatewayDetails.gateway.Name),
		)
		routeDetails.httpRoute.Status.Parents = append(routeDetails.httpRoute.Status.Parents, parentStatus)
	}

	if err := m.client.Status().Update(ctx, &routeDetails.httpRoute); err != nil {
		return fmt.Errorf("failed to update status for HTTProute %s: %w", routeDetails.httpRoute.Name, err)
	}

	return nil
}

// httpRouteModelDeps defines the dependencies required for the httpRouteModel.
type httpRouteModelDeps struct {
	dig.In

	K8sClient    k8sClient
	RootLogger   *slog.Logger
	GatewayModel gatewayModel
}

// newHTTPRouteModel creates a new instance of httpRouteModel.
func newHTTPRouteModel(deps httpRouteModelDeps) httpRouteModel {
	return &httpRouteModelImpl{
		client:       deps.K8sClient,
		logger:       deps.RootLogger.With("component", "httproute-model"),
		gatewayModel: deps.GatewayModel,
	}
}
