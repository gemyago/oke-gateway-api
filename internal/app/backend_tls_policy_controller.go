package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

const backendTLSPolicyCleanupRequeueAfter = 30 * time.Second

type BackendTLSPolicyController struct {
	logger *slog.Logger
	client k8sClient
	model  backendTLSPolicyModel
}

type BackendTLSPolicyControllerDeps struct {
	dig.In

	RootLogger *slog.Logger
	K8sClient  k8sClient
	Model      backendTLSPolicyModel
}

func NewBackendTLSPolicyController(deps BackendTLSPolicyControllerDeps) *BackendTLSPolicyController {
	return &BackendTLSPolicyController{
		logger: deps.RootLogger.WithGroup("backend-tls-policy-controller"),
		client: deps.K8sClient,
		model:  deps.Model,
	}
}

func (c *BackendTLSPolicyController) Reconcile(
	ctx context.Context,
	req reconcile.Request,
) (reconcile.Result, error) {
	c.logger.InfoContext(ctx, fmt.Sprintf("Processing reconciliation for BackendTLSPolicy %s", req.NamespacedName))

	var policy gatewayv1.BackendTLSPolicy
	if err := c.client.Get(ctx, apitypes.NamespacedName{
		Namespace: req.Namespace,
		Name:      req.Name,
	}, &policy); err != nil {
		if apierrors.IsNotFound(err) {
			return reconcile.Result{}, nil
		}
		return reconcile.Result{}, fmt.Errorf("failed to get BackendTLSPolicy %s: %w", req.NamespacedName, err)
	}
	if policy.DeletionTimestamp != nil {
		if err := c.model.cleanupDeletingPolicy(ctx, policy); err != nil {
			if errors.Is(err, errBackendTLSCABundleStillAssociated) {
				c.logger.InfoContext(ctx,
					"BackendTLSPolicy cleanup is waiting for OCI CA bundle associations to be removed",
					slog.String("policy", req.NamespacedName.String()),
					slog.Duration("requeueAfter", backendTLSPolicyCleanupRequeueAfter),
				)
				return reconcile.Result{RequeueAfter: backendTLSPolicyCleanupRequeueAfter}, nil
			}
			return reconcile.Result{}, fmt.Errorf("failed to cleanup BackendTLSPolicy %s: %w", req.NamespacedName, err)
		}
	}
	return reconcile.Result{}, nil
}
