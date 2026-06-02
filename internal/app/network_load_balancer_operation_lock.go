package app

import "sync"

type networkLoadBalancerOperationLocks struct {
	mutexes sync.Map
}

func newNetworkLoadBalancerOperationLocks() *networkLoadBalancerOperationLocks {
	return &networkLoadBalancerOperationLocks{}
}

func networkLoadBalancerOperationLockID(details resolvedGatewayDetails) *string {
	if nlbID := details.gateway.Annotations[NetworkLoadBalancerGatewayIDAnnotation]; nlbID != "" {
		return &nlbID
	}
	if nlbID := details.config.Spec.LoadBalancerID; nlbID != "" {
		return &nlbID
	}
	return nil
}

func (l *networkLoadBalancerOperationLocks) withLock(networkLoadBalancerID *string, operation func() error) error {
	if networkLoadBalancerID == nil || *networkLoadBalancerID == "" {
		return operation()
	}

	value, loaded := l.mutexes.LoadOrStore(*networkLoadBalancerID, &sync.Mutex{})
	mutex, ok := value.(*sync.Mutex)
	if !ok {
		if loaded {
			l.mutexes.Delete(*networkLoadBalancerID)
		}
		return operation()
	}
	mutex.Lock()
	defer mutex.Unlock()

	return operation()
}
