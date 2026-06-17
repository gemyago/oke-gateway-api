package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/networkloadbalancer"
	"github.com/samber/lo"
	"go.uber.org/dig"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"

	"github.com/gemyago/oke-gateway-api/internal/types"
)

const networkLoadBalancerHealthCheckRetries = 3
const networkLoadBalancerHealthCheckTimeoutMillis = 3000
const networkLoadBalancerHealthCheckIntervalMillis = 10000

type networkLoadBalancerGatewayModel interface {
	resolveReconcileRequest(
		ctx context.Context,
		req reconcile.Request,
		receiver *resolvedGatewayDetails,
	) (bool, error)

	ensureNetworkLoadBalancer(
		ctx context.Context,
		data *resolvedGatewayDetails,
	) (*networkloadbalancer.NetworkLoadBalancer, error)

	getNetworkLoadBalancer(
		ctx context.Context,
		data *resolvedGatewayDetails,
	) (*networkloadbalancer.NetworkLoadBalancer, error)

	programGateway(ctx context.Context, data *resolvedGatewayDetails) error

	deprovisionGateway(ctx context.Context, data *resolvedGatewayDetails) error

	isProgrammed(ctx context.Context, data *resolvedGatewayDetails) bool

	setProgrammed(ctx context.Context, data *resolvedGatewayDetails, nlb *networkloadbalancer.NetworkLoadBalancer) error
}

type networkLoadBalancerGatewayModelImpl struct {
	client              k8sClient
	logger              *slog.Logger
	ociClient           ociNetworkLoadBalancerClient
	resourcesModel      resourcesModel
	workRequestsWatcher workRequestsWatcher
	operationLocks      *networkLoadBalancerOperationLocks
}

func networkLoadBalancerBackendSetName(listener gatewayv1.Listener) string {
	return "bs_" + strings.ReplaceAll(string(listener.Name), "-", "_")
}

func networkLoadBalancerListenerProtocol(
	protocol gatewayv1.ProtocolType,
) (networkloadbalancer.ListenerProtocolsEnum, bool) {
	switch protocol {
	case gatewayv1.TCPProtocolType:
		return networkloadbalancer.ListenerProtocolsTcp, true
	case gatewayv1.UDPProtocolType:
		return networkloadbalancer.ListenerProtocolsUdp, true
	case gatewayv1.HTTPProtocolType, gatewayv1.HTTPSProtocolType, gatewayv1.TLSProtocolType:
		return "", false
	default:
		return "", false
	}
}

func networkLoadBalancerHealthCheckerDetails(
	_ gatewayv1.ProtocolType,
	port *int,
) networkloadbalancer.HealthCheckerDetails {
	healthChecker := networkloadbalancer.HealthCheckerDetails{
		Protocol:         networkloadbalancer.HealthCheckProtocolsTcp,
		Port:             port,
		Retries:          new(networkLoadBalancerHealthCheckRetries),
		TimeoutInMillis:  new(networkLoadBalancerHealthCheckTimeoutMillis),
		IntervalInMillis: new(networkLoadBalancerHealthCheckIntervalMillis),
	}
	return healthChecker
}

func networkLoadBalancerHealthCheckerMatches(
	actual *networkloadbalancer.HealthChecker,
	desired networkloadbalancer.HealthCheckerDetails,
) bool {
	if actual == nil {
		return false
	}
	return actual.Protocol == desired.Protocol &&
		lo.FromPtr(actual.Port) == lo.FromPtr(desired.Port) &&
		lo.FromPtr(actual.Retries) == lo.FromPtr(desired.Retries) &&
		lo.FromPtr(actual.TimeoutInMillis) == lo.FromPtr(desired.TimeoutInMillis) &&
		lo.FromPtr(actual.IntervalInMillis) == lo.FromPtr(desired.IntervalInMillis)
}

func networkLoadBalancerBackendSetMatches(
	actual networkloadbalancer.BackendSet,
	desiredPolicy networkloadbalancer.NetworkLoadBalancingPolicyEnum,
	desiredHealthChecker networkloadbalancer.HealthCheckerDetails,
) bool {
	return actual.Policy == desiredPolicy &&
		!lo.FromPtr(actual.IsPreserveSource) &&
		networkLoadBalancerHealthCheckerMatches(actual.HealthChecker, desiredHealthChecker)
}

func networkLoadBalancerListenerMatches(
	actual networkloadbalancer.Listener,
	desiredProtocol networkloadbalancer.ListenerProtocolsEnum,
	desiredPort int,
	desiredBackendSetName string,
) bool {
	return actual.Protocol == desiredProtocol &&
		lo.FromPtr(actual.Port) == desiredPort &&
		lo.FromPtr(actual.DefaultBackendSetName) == desiredBackendSetName
}

func gatewayStatusAddressesFromNetworkLoadBalancer(
	nlb *networkloadbalancer.NetworkLoadBalancer,
) []gatewayv1.GatewayStatusAddress {
	if nlb == nil || len(nlb.IpAddresses) == 0 {
		return nil
	}

	values := make([]string, 0, len(nlb.IpAddresses))
	for _, ipAddress := range nlb.IpAddresses {
		if ipAddress.IpAddress == nil || *ipAddress.IpAddress == "" {
			continue
		}
		values = append(values, *ipAddress.IpAddress)
	}
	return gatewayStatusAddressesFromValues(values)
}

func networkLoadBalancerID(config types.GatewayConfig) (string, error) {
	if config.Spec.LoadBalancerID == "" {
		return "", &resourceStatusError{
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			reason:        string(gatewayv1.GatewayReasonInvalidParameters),
			message:       "spec.loadBalancerId is required for OCI Network Load Balancer gateways",
		}
	}
	return config.Spec.LoadBalancerID, nil
}

func (m *networkLoadBalancerGatewayModelImpl) resolveReconcileRequest(
	ctx context.Context,
	req reconcile.Request,
	receiver *resolvedGatewayDetails,
) (bool, error) {
	if err := m.client.Get(ctx, req.NamespacedName, &receiver.gateway); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("Gateway %s not found", req.NamespacedName))
			return false, nil
		}
		return false, fmt.Errorf("failed to get Gateway %s: %w", req.NamespacedName, err)
	}

	if err := m.client.Get(ctx, apitypes.NamespacedName{
		Name: string(receiver.gateway.Spec.GatewayClassName),
	}, &receiver.gatewayClass); err != nil {
		if apierrors.IsNotFound(err) {
			m.logger.InfoContext(ctx, fmt.Sprintf("GatewayClass %s not found", receiver.gateway.Spec.GatewayClassName))
			return false, nil
		}
		return false, fmt.Errorf("failed to get GatewayClass %s: %w", receiver.gateway.Spec.GatewayClassName, err)
	}

	if receiver.gatewayClass.Spec.ControllerName != gatewayv1.GatewayController(
		NetworkLoadBalancerControllerClassName,
	) {
		m.logger.DebugContext(ctx,
			"GatewayClass is not managed by the Network Load Balancer controller",
			slog.String("gatewayClass", string(receiver.gateway.Spec.GatewayClassName)),
			slog.String("actualControllerName", string(receiver.gatewayClass.Spec.ControllerName)),
		)
		return false, nil
	}

	if receiver.gateway.Spec.Infrastructure == nil || receiver.gateway.Spec.Infrastructure.ParametersRef == nil {
		if receiver.gateway.DeletionTimestamp != nil &&
			controllerutil.ContainsFinalizer(&receiver.gateway, NetworkLoadBalancerGatewayProgrammedFinalizer) &&
			receiver.gateway.Annotations[NetworkLoadBalancerGatewayIDAnnotation] != "" {
			return true, nil
		}
		return false, &resourceStatusError{
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			reason:        string(gatewayv1.GatewayReasonInvalidParameters),
			message:       "spec.infrastructure is missing parametersRef",
		}
	}

	configName := apitypes.NamespacedName{
		Namespace: receiver.gateway.Namespace,
		Name:      receiver.gateway.Spec.Infrastructure.ParametersRef.Name,
	}
	if err := m.client.Get(ctx, configName, &receiver.config); err != nil {
		if apierrors.IsNotFound(err) {
			if receiver.gateway.DeletionTimestamp != nil &&
				controllerutil.ContainsFinalizer(&receiver.gateway, NetworkLoadBalancerGatewayProgrammedFinalizer) &&
				receiver.gateway.Annotations[NetworkLoadBalancerGatewayIDAnnotation] != "" {
				return true, nil
			}
			return false, &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionAccepted),
				reason:        string(gatewayv1.GatewayReasonInvalidParameters),
				message:       "spec.infrastructure is pointing to a non-existent GatewayConfig",
			}
		}
		return false, fmt.Errorf("failed to get GatewayConfig %s: %w", configName, err)
	}

	return true, nil
}

func (m *networkLoadBalancerGatewayModelImpl) ensureNetworkLoadBalancer(
	ctx context.Context,
	data *resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	nlbID, err := networkLoadBalancerID(data.config)
	if err != nil {
		return nil, err
	}
	return m.getNetworkLoadBalancerByID(ctx, nlbID)
}

func (m *networkLoadBalancerGatewayModelImpl) getNetworkLoadBalancerByID(
	ctx context.Context,
	id string,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	response, err := m.ociClient.GetNetworkLoadBalancer(ctx, networkloadbalancer.GetNetworkLoadBalancerRequest{
		NetworkLoadBalancerId: new(id),
	})
	if err != nil {
		if serviceErr, ok := common.IsServiceError(err); ok &&
			serviceErr.GetHTTPStatusCode() == http.StatusNotFound {
			return nil, &resourceStatusError{
				conditionType: string(gatewayv1.GatewayConditionProgrammed),
				reason:        string(gatewayv1.GatewayReasonPending),
				message:       fmt.Sprintf("referenced OCI Network Load Balancer %s not found", id),
			}
		}
		return nil, fmt.Errorf("failed to get OCI Network Load Balancer %s: %w", id, err)
	}
	return &response.NetworkLoadBalancer, nil
}

func (m *networkLoadBalancerGatewayModelImpl) getNetworkLoadBalancer(
	ctx context.Context,
	data *resolvedGatewayDetails,
) (*networkloadbalancer.NetworkLoadBalancer, error) {
	nlbID, err := networkLoadBalancerID(data.config)
	if err != nil {
		return nil, err
	}
	return m.getNetworkLoadBalancerByID(ctx, nlbID)
}

func (m *networkLoadBalancerGatewayModelImpl) deprovisionGateway(
	ctx context.Context,
	data *resolvedGatewayDetails,
) error {
	gatewayToUpdate := data.gateway.DeepCopy()
	controllerutil.RemoveFinalizer(gatewayToUpdate, NetworkLoadBalancerGatewayProgrammedFinalizer)
	annotations := gatewayToUpdate.GetAnnotations()
	delete(annotations, NetworkLoadBalancerGatewayIDAnnotation)
	gatewayToUpdate.SetAnnotations(annotations)
	if updateErr := m.client.Update(ctx, gatewayToUpdate); updateErr != nil {
		return fmt.Errorf("failed to remove finalizer from Gateway %s/%s: %w",
			gatewayToUpdate.Namespace,
			gatewayToUpdate.Name,
			updateErr,
		)
	}
	return nil
}

func (m *networkLoadBalancerGatewayModelImpl) reconcileListenerBackendSet(
	ctx context.Context,
	nlb networkloadbalancer.NetworkLoadBalancer,
	listener gatewayv1.Listener,
) error {
	backendSetName := networkLoadBalancerBackendSetName(listener)
	port := int(listener.Port)
	healthChecker := networkLoadBalancerHealthCheckerDetails(listener.Protocol, new(port))

	if _, exists := nlb.BackendSets[backendSetName]; exists {
		m.logger.DebugContext(ctx, "Network Load Balancer backend set already up to date",
			slog.String("backendSetName", backendSetName),
		)
		return nil
	}

	response, err := m.ociClient.CreateBackendSet(ctx, networkloadbalancer.CreateBackendSetRequest{
		NetworkLoadBalancerId: nlb.Id,
		CreateBackendSetDetails: networkloadbalancer.CreateBackendSetDetails{
			Name:             new(backendSetName),
			Policy:           networkloadbalancer.NetworkLoadBalancingPolicyFiveTuple,
			HealthChecker:    &healthChecker,
			IsPreserveSource: new(false),
		},
	})
	if err != nil {
		if busyErr := networkLoadBalancerBusyErrorFromOCI(nlb.Id, err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to create OCI Network Load Balancer backend set %s: %w", backendSetName, err)
	}
	if response.OpcWorkRequestId == nil {
		return fmt.Errorf(
			"failed to create OCI Network Load Balancer backend set %s: missing work request id",
			backendSetName,
		)
	}
	return m.workRequestsWatcher.WaitFor(ctx, *response.OpcWorkRequestId)
}

func (m *networkLoadBalancerGatewayModelImpl) reconcileListener(
	ctx context.Context,
	nlb networkloadbalancer.NetworkLoadBalancer,
	listener gatewayv1.Listener,
) error {
	protocol, ok := networkLoadBalancerListenerProtocol(listener.Protocol)
	if !ok {
		return &resourceStatusError{
			conditionType: string(gatewayv1.GatewayConditionAccepted),
			reason:        string(gatewayv1.GatewayReasonInvalid),
			message: fmt.Sprintf(
				"listener %s uses unsupported protocol %s for OCI Network Load Balancer",
				listener.Name,
				listener.Protocol,
			),
		}
	}

	if err := m.reconcileListenerBackendSet(ctx, nlb, listener); err != nil {
		return err
	}

	listenerName := string(listener.Name)
	backendSetName := networkLoadBalancerBackendSetName(listener)
	port := int(listener.Port)
	existingListener, exists := nlb.Listeners[listenerName]
	if exists && networkLoadBalancerListenerMatches(existingListener, protocol, port, backendSetName) {
		m.logger.DebugContext(ctx, "Network Load Balancer listener already up to date",
			slog.String("listenerName", listenerName),
		)
		return nil
	}
	if exists {
		m.logger.InfoContext(ctx, "Updating OCI Network Load Balancer listener",
			slog.String("networkLoadBalancerId", lo.FromPtr(nlb.Id)),
			slog.String("listenerName", listenerName),
			slog.String("backendSetName", backendSetName),
			slog.String("protocol", string(protocol)),
			slog.Int("port", port),
		)

		response, err := m.ociClient.UpdateListener(ctx, networkloadbalancer.UpdateListenerRequest{
			NetworkLoadBalancerId: nlb.Id,
			ListenerName:          new(listenerName),
			UpdateListenerDetails: networkloadbalancer.UpdateListenerDetails{
				DefaultBackendSetName: new(backendSetName),
				Port:                  new(port),
				Protocol:              protocol,
			},
		})
		if err != nil {
			if busyErr := networkLoadBalancerBusyErrorFromOCI(nlb.Id, err); busyErr != nil {
				return busyErr
			}
			return fmt.Errorf("failed to update OCI Network Load Balancer listener %s: %w", listenerName, err)
		}
		if response.OpcWorkRequestId == nil {
			return fmt.Errorf(
				"failed to update OCI Network Load Balancer listener %s: missing work request id",
				listenerName,
			)
		}
		return m.workRequestsWatcher.WaitFor(ctx, *response.OpcWorkRequestId)
	}

	m.logger.InfoContext(ctx, "Creating OCI Network Load Balancer listener",
		slog.String("networkLoadBalancerId", lo.FromPtr(nlb.Id)),
		slog.String("listenerName", listenerName),
		slog.String("backendSetName", backendSetName),
		slog.String("protocol", string(protocol)),
		slog.Int("port", port),
	)

	response, err := m.ociClient.CreateListener(ctx, networkloadbalancer.CreateListenerRequest{
		NetworkLoadBalancerId: nlb.Id,
		CreateListenerDetails: networkloadbalancer.CreateListenerDetails{
			Name:                  new(listenerName),
			DefaultBackendSetName: new(backendSetName),
			Port:                  new(port),
			Protocol:              protocol,
		},
	})
	if err != nil {
		if busyErr := networkLoadBalancerBusyErrorFromOCI(nlb.Id, err); busyErr != nil {
			return busyErr
		}
		return fmt.Errorf("failed to create OCI Network Load Balancer listener %s: %w", listenerName, err)
	}
	if response.OpcWorkRequestId == nil {
		return fmt.Errorf(
			"failed to create OCI Network Load Balancer listener %s: missing work request id",
			listenerName,
		)
	}
	return m.workRequestsWatcher.WaitFor(ctx, *response.OpcWorkRequestId)
}

func desiredNetworkLoadBalancerListenerNames(
	listeners []gatewayv1.Listener,
) map[string]struct{} {
	desired := make(map[string]struct{})
	for _, listener := range listeners {
		if _, supported := networkLoadBalancerListenerProtocol(listener.Protocol); !supported {
			continue
		}
		desired[string(listener.Name)] = struct{}{}
	}
	return desired
}

func desiredNetworkLoadBalancerBackendSetNames(
	listeners []gatewayv1.Listener,
) map[string]struct{} {
	desired := make(map[string]struct{})
	for _, listener := range listeners {
		if _, supported := networkLoadBalancerListenerProtocol(listener.Protocol); !supported {
			continue
		}
		desired[networkLoadBalancerBackendSetName(listener)] = struct{}{}
	}
	return desired
}

func (m *networkLoadBalancerGatewayModelImpl) removeMissingListeners(
	ctx context.Context,
	nlb networkloadbalancer.NetworkLoadBalancer,
	gatewayListeners []gatewayv1.Listener,
) error {
	desiredListeners := desiredNetworkLoadBalancerListenerNames(gatewayListeners)
	return removeMissingNetworkLoadBalancerResources(
		ctx,
		m.logger,
		m.workRequestsWatcher,
		nlb.Id,
		nlb.Listeners,
		desiredListeners,
		nlbResourceCleanup{
			kind:       "listener",
			logMessage: "Deleting stale OCI Network Load Balancer listener",
			delete: func(resourceName string) (*string, error) {
				response, err := m.ociClient.DeleteListener(ctx, networkloadbalancer.DeleteListenerRequest{
					NetworkLoadBalancerId: nlb.Id,
					ListenerName:          new(resourceName),
				})
				if err != nil {
					return nil, err
				}
				return response.OpcWorkRequestId, nil
			},
		},
	)
}

func (m *networkLoadBalancerGatewayModelImpl) removeMissingBackendSets(
	ctx context.Context,
	nlb networkloadbalancer.NetworkLoadBalancer,
	gatewayListeners []gatewayv1.Listener,
) error {
	desiredBackendSets := desiredNetworkLoadBalancerBackendSetNames(gatewayListeners)
	return removeMissingNetworkLoadBalancerResources(
		ctx,
		m.logger,
		m.workRequestsWatcher,
		nlb.Id,
		nlb.BackendSets,
		desiredBackendSets,
		nlbResourceCleanup{
			kind:       "backend set",
			logMessage: "Deleting stale OCI Network Load Balancer backend set",
			delete: func(resourceName string) (*string, error) {
				response, err := m.ociClient.DeleteBackendSet(ctx, networkloadbalancer.DeleteBackendSetRequest{
					NetworkLoadBalancerId: nlb.Id,
					BackendSetName:        new(resourceName),
				})
				if err != nil {
					return nil, err
				}
				return response.OpcWorkRequestId, nil
			},
		},
	)
}

type nlbResourceCleanup struct {
	kind       string
	logMessage string
	delete     func(resourceName string) (*string, error)
}

func removeMissingNetworkLoadBalancerResources[T any](
	ctx context.Context,
	logger *slog.Logger,
	workRequestsWatcher workRequestsWatcher,
	networkLoadBalancerID *string,
	current map[string]T,
	desired map[string]struct{},
	cleanup nlbResourceCleanup,
) error {
	for resourceName := range current {
		if _, found := desired[resourceName]; found {
			continue
		}

		logger.InfoContext(ctx, cleanup.logMessage,
			slog.String("networkLoadBalancerId", lo.FromPtr(networkLoadBalancerID)),
			slog.String("resourceName", resourceName),
		)
		workRequestID, err := cleanup.delete(resourceName)
		if err != nil {
			if busyErr := networkLoadBalancerBusyErrorFromOCI(networkLoadBalancerID, err); busyErr != nil {
				return busyErr
			}
			return fmt.Errorf(
				"failed to delete stale OCI Network Load Balancer %s %s: %w",
				cleanup.kind,
				resourceName,
				err,
			)
		}
		if workRequestID == nil {
			return fmt.Errorf("failed to delete stale %s %s: missing work request id", cleanup.kind, resourceName)
		}
		if err = workRequestsWatcher.WaitFor(ctx, *workRequestID); err != nil {
			return fmt.Errorf("failed waiting for stale %s %s deletion: %w", cleanup.kind, resourceName, err)
		}
	}
	return nil
}

func (m *networkLoadBalancerGatewayModelImpl) programGateway(
	ctx context.Context,
	data *resolvedGatewayDetails,
) error {
	nlb, err := m.ensureNetworkLoadBalancer(ctx, data)
	if err != nil {
		return err
	}
	if nlb.Id == nil {
		return errors.New("OCI Network Load Balancer id is empty")
	}
	if busyErr := networkLoadBalancerBusyErrorFromState(nlb); busyErr != nil {
		return busyErr
	}
	annotations := data.gateway.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations[NetworkLoadBalancerGatewayIDAnnotation] = *nlb.Id
	data.gateway.SetAnnotations(annotations)

	if nlb.Listeners == nil {
		nlb.Listeners = map[string]networkloadbalancer.Listener{}
	}
	if nlb.BackendSets == nil {
		nlb.BackendSets = map[string]networkloadbalancer.BackendSet{}
	}

	return m.operationLocks.withLock(nlb.Id, func() error {
		for _, listener := range data.gateway.Spec.Listeners {
			if err = m.reconcileListener(ctx, *nlb, listener); err != nil {
				return fmt.Errorf("failed to reconcile Network Load Balancer listener %s: %w", listener.Name, err)
			}
		}
		if err = m.removeMissingListeners(ctx, *nlb, data.gateway.Spec.Listeners); err != nil {
			return err
		}
		if err = m.removeMissingBackendSets(ctx, *nlb, data.gateway.Spec.Listeners); err != nil {
			return err
		}
		return nil
	})
}

func (m *networkLoadBalancerGatewayModelImpl) isProgrammed(_ context.Context, data *resolvedGatewayDetails) bool {
	annotations := map[string]string{
		NetworkLoadBalancerGatewayProgrammingRevisionAnnotation: NetworkLoadBalancerGatewayProgrammingRevisionValue,
	}
	if data.config.Spec.LoadBalancerID != "" {
		annotations[NetworkLoadBalancerGatewayIDAnnotation] = data.config.Spec.LoadBalancerID
	}

	return m.resourcesModel.isConditionSet(isConditionSetParams{
		resource:      &data.gateway,
		conditions:    data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		annotations:   annotations,
	})
}

func (m *networkLoadBalancerGatewayModelImpl) setProgrammed(
	ctx context.Context,
	data *resolvedGatewayDetails,
	nlb *networkloadbalancer.NetworkLoadBalancer,
) error {
	if m.client != nil {
		if err := m.client.Get(ctx, apitypes.NamespacedName{
			Namespace: data.gateway.Namespace,
			Name:      data.gateway.Name,
		}, &data.gateway); err != nil {
			return fmt.Errorf(
				"failed to refresh Gateway %s before setting programmed condition: %w",
				data.gateway.Name,
				err,
			)
		}
	}

	data.gateway.Status.Addresses = gatewayStatusAddressesFromNetworkLoadBalancer(nlb)
	annotations := map[string]string{
		NetworkLoadBalancerGatewayProgrammingRevisionAnnotation: NetworkLoadBalancerGatewayProgrammingRevisionValue,
	}
	if nlb != nil && nlb.Id != nil {
		annotations[NetworkLoadBalancerGatewayIDAnnotation] = *nlb.Id
	}
	if err := m.resourcesModel.setCondition(ctx, setConditionParams{
		resource:      &data.gateway,
		conditions:    &data.gateway.Status.Conditions,
		conditionType: string(gatewayv1.GatewayConditionProgrammed),
		status:        metav1.ConditionTrue,
		reason:        string(gatewayv1.GatewayReasonProgrammed),
		message: fmt.Sprintf(
			"Gateway %s programmed by %s",
			data.gateway.Name,
			NetworkLoadBalancerControllerClassName,
		),
		annotations: annotations,
		finalizer:   NetworkLoadBalancerGatewayProgrammedFinalizer,
	}); err != nil {
		return fmt.Errorf("failed to set programmed condition for Gateway %s: %w", data.gateway.Name, err)
	}
	return nil
}

type networkLoadBalancerGatewayModelDeps struct {
	dig.In

	RootLogger          *slog.Logger
	K8sClient           k8sClient
	OciClient           ociNetworkLoadBalancerClient
	ResourcesModel      resourcesModel
	WorkRequestsWatcher workRequestsWatcher `name:"networkLoadBalancerWorkRequestsWatcher"`
	OperationLocks      *networkLoadBalancerOperationLocks
}

func newNetworkLoadBalancerGatewayModel(deps networkLoadBalancerGatewayModelDeps) *networkLoadBalancerGatewayModelImpl {
	operationLocks := deps.OperationLocks
	if operationLocks == nil {
		operationLocks = newNetworkLoadBalancerOperationLocks()
	}
	workRequestsWatcher := deps.WorkRequestsWatcher
	if workRequestsWatcher == nil {
		workRequestsWatcher = noopWorkRequestsWatcher{}
	}
	return &networkLoadBalancerGatewayModelImpl{
		client:              deps.K8sClient,
		logger:              deps.RootLogger.WithGroup("network-load-balancer-gateway-model"),
		ociClient:           deps.OciClient,
		resourcesModel:      deps.ResourcesModel,
		workRequestsWatcher: workRequestsWatcher,
		operationLocks:      operationLocks,
	}
}
