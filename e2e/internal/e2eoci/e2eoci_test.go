package e2eoci

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/oracle/oci-go-sdk/v65/common"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"

	"github.com/gemyago/oke-gateway-api/e2e/internal/config"
)

func TestNewLoadBalancerClient(t *testing.T) {
	t.Parallel()

	t.Run("uses the default provider when no OCI overrides are set", func(t *testing.T) {
		t.Parallel()

		defaultCalls := 0
		customCalls := 0

		client, err := NewLoadBalancerClient(config.OCIConfig{}, &ClientFactoryOptions{
			defaultConfigProvider: func() common.ConfigurationProvider {
				defaultCalls++
				return nil
			},
			customProfileConfigProvider: func(_, _ string) common.ConfigurationProvider {
				customCalls++
				return nil
			},
			newLoadBalancerClient: func(common.ConfigurationProvider) (loadbalancer.LoadBalancerClient, error) {
				return loadbalancer.LoadBalancerClient{}, nil
			},
		})
		assertNoError(t, err)
		assertTrue(t, client != nil)
		assertEqual(t, 1, defaultCalls)
		assertEqual(t, 0, customCalls)
	})

	t.Run("uses the custom profile provider when OCI overrides are set", func(t *testing.T) {
		t.Parallel()

		var gotPath string
		var gotProfile string

		client, err := NewLoadBalancerClient(config.OCIConfig{
			ConfigFile: "/tmp/oci-config",
		}, &ClientFactoryOptions{
			defaultConfigProvider: func() common.ConfigurationProvider {
				t.Fatal("default provider should not be used")
				return nil
			},
			customProfileConfigProvider: func(path string, profile string) common.ConfigurationProvider {
				gotPath = path
				gotProfile = profile
				return nil
			},
			newLoadBalancerClient: func(common.ConfigurationProvider) (loadbalancer.LoadBalancerClient, error) {
				return loadbalancer.LoadBalancerClient{}, nil
			},
		})
		assertNoError(t, err)
		assertTrue(t, client != nil)
		assertEqual(t, "/tmp/oci-config", gotPath)
		assertEqual(t, defaultOCIProfile, gotProfile)
	})

	t.Run("wraps client construction errors", func(t *testing.T) {
		t.Parallel()

		wantErr := errors.New("boom")
		_, err := NewLoadBalancerClient(config.OCIConfig{}, &ClientFactoryOptions{
			defaultConfigProvider: func() common.ConfigurationProvider {
				return nil
			},
			customProfileConfigProvider: func(_, _ string) common.ConfigurationProvider {
				return nil
			},
			newLoadBalancerClient: func(common.ConfigurationProvider) (loadbalancer.LoadBalancerClient, error) {
				return loadbalancer.LoadBalancerClient{}, wantErr
			},
		})
		assertErrorContains(t, err, "create OCI load balancer client")
		assertErrorContains(t, err, wantErr.Error())
	})
}

func TestWorkRequestWaiter(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)

	t.Run("waits until the work request succeeds", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		waiter := NewWorkRequestWaiter(&fakeLoadBalancerClient{
			getWorkRequest: func(
				_ context.Context,
				request loadbalancer.GetWorkRequestRequest,
			) (loadbalancer.GetWorkRequestResponse, error) {
				callCount++
				assertEqual(t, "wr-1", stringValue(request.WorkRequestId))

				if callCount == 1 {
					return loadbalancer.GetWorkRequestResponse{
						WorkRequest: loadbalancer.WorkRequest{
							LifecycleState: loadbalancer.WorkRequestLifecycleStateInProgress,
						},
					}, nil
				}

				return loadbalancer.GetWorkRequestResponse{
					WorkRequest: loadbalancer.WorkRequest{
						LifecycleState: loadbalancer.WorkRequestLifecycleStateSucceeded,
					},
				}, nil
			},
		}, logger, &WorkRequestWaiterOptions{
			PollInterval: time.Millisecond,
			WaitTimeout:  50 * time.Millisecond,
		})

		err := waiter.Wait(t.Context(), "wr-1")
		assertNoError(t, err)
		assertEqual(t, 2, callCount)
	})

	t.Run("returns failure details when the work request fails", func(t *testing.T) {
		t.Parallel()

		waiter := NewWorkRequestWaiter(&fakeLoadBalancerClient{
			getWorkRequest: func(
				_ context.Context,
				_ loadbalancer.GetWorkRequestRequest,
			) (loadbalancer.GetWorkRequestResponse, error) {
				message := "listener deletion failed"
				detailMessage := "listener still referenced"
				return loadbalancer.GetWorkRequestResponse{
					WorkRequest: loadbalancer.WorkRequest{
						LifecycleState: loadbalancer.WorkRequestLifecycleStateFailed,
						Message:        &message,
						ErrorDetails: []loadbalancer.WorkRequestError{
							{
								ErrorCode: loadbalancer.WorkRequestErrorErrorCodeBadInput,
								Message:   &detailMessage,
							},
						},
					},
				}, nil
			},
		}, logger, &WorkRequestWaiterOptions{
			PollInterval: time.Millisecond,
			WaitTimeout:  50 * time.Millisecond,
		})

		err := waiter.Wait(t.Context(), "wr-2")
		assertErrorContains(t, err, `work request "wr-2" failed`)
		assertErrorContains(t, err, "listener deletion failed")
		assertErrorContains(t, err, "BAD_INPUT: listener still referenced")
	})

	t.Run("rejects empty work request ids", func(t *testing.T) {
		t.Parallel()

		waiter := NewWorkRequestWaiter(&fakeLoadBalancerClient{}, logger, nil)
		err := waiter.Wait(t.Context(), "   ")
		assertErrorContains(t, err, "work request id is required")
	})
}

func TestLoadBalancerCleaner(t *testing.T) {
	t.Parallel()

	logger := slog.New(slog.DiscardHandler)

	t.Run("inspect validates the load balancer and selects a stable public ip", func(t *testing.T) {
		t.Parallel()

		cleaner := NewLoadBalancerCleaner(&fakeLoadBalancerClient{
			getLoadBalancer: func(
				_ context.Context,
				request loadbalancer.GetLoadBalancerRequest,
			) (loadbalancer.GetLoadBalancerResponse, error) {
				isPrivate := false
				assertEqual(t, "ocid1.loadbalancer.oc1..inspect", stringValue(request.LoadBalancerId))
				return loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						LifecycleState: loadbalancer.LoadBalancerLifecycleStateActive,
						IsPrivate:      &isPrivate,
						IpAddresses: []loadbalancer.IpAddress{
							makeIPAddress("203.0.113.20", true),
							makeIPAddress("10.0.0.8", false),
							makeIPAddress("203.0.113.10", true),
						},
						Listeners: map[string]loadbalancer.Listener{
							"listener-b": {},
							"listener-a": {},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							"policy-b": {},
							"policy-a": {},
						},
						BackendSets: map[string]loadbalancer.BackendSet{
							"backend-b": {},
							"backend-a": {},
						},
						Certificates: map[string]loadbalancer.Certificate{
							"cert-b": {},
							"cert-a": {},
						},
					},
				}, nil
			},
		}, logger, nil)

		result, err := cleaner.Inspect(t.Context(), "ocid1.loadbalancer.oc1..inspect")
		assertNoError(t, err)
		assertEqual(t, "203.0.113.10", result.PublicIP)
		assertSliceEqual(t, []string{"listener-a", "listener-b"}, result.ListenerNames)
		assertSliceEqual(t, []string{"policy-a", "policy-b"}, result.RoutingPolicyNames)
		assertSliceEqual(t, []string{"backend-a", "backend-b"}, result.BackendSetNames)
		assertSliceEqual(t, []string{"cert-a", "cert-b"}, result.CertificateNames)
	})

	t.Run("inspect rejects load balancers without a public ip", func(t *testing.T) {
		t.Parallel()

		cleaner := NewLoadBalancerCleaner(&fakeLoadBalancerClient{
			getLoadBalancer: func(
				_ context.Context,
				_ loadbalancer.GetLoadBalancerRequest,
			) (loadbalancer.GetLoadBalancerResponse, error) {
				isPrivate := true
				return loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						IsPrivate: &isPrivate,
						IpAddresses: []loadbalancer.IpAddress{
							makeIPAddress("10.0.0.8", false),
						},
					},
				}, nil
			},
		}, logger, nil)

		_, err := cleaner.Inspect(t.Context(), "ocid1.loadbalancer.oc1..private")
		assertErrorContains(t, err, "failed preflight validation")
		assertErrorContains(t, err, "no public IP addresses")
	})

	t.Run("cleanup deletes listeners then routing policies then backend sets then certificates", func(t *testing.T) {
		t.Parallel()

		var operations []string
		workRequests := map[string]loadbalancer.WorkRequestLifecycleStateEnum{
			"wr-listener-a": loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-listener-b": loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-policy-a":   loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-backend-a":  loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-backend-b":  loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-cert-a":     loadbalancer.WorkRequestLifecycleStateSucceeded,
			"wr-cert-b":     loadbalancer.WorkRequestLifecycleStateSucceeded,
		}

		cleaner := NewLoadBalancerCleaner(&fakeLoadBalancerClient{
			getLoadBalancer: func(
				_ context.Context,
				_ loadbalancer.GetLoadBalancerRequest,
			) (loadbalancer.GetLoadBalancerResponse, error) {
				isPrivate := false
				return loadbalancer.GetLoadBalancerResponse{
					LoadBalancer: loadbalancer.LoadBalancer{
						IsPrivate: &isPrivate,
						IpAddresses: []loadbalancer.IpAddress{
							makeIPAddress("203.0.113.50", true),
						},
						Listeners: map[string]loadbalancer.Listener{
							"listener-b": {},
							"listener-a": {},
						},
						RoutingPolicies: map[string]loadbalancer.RoutingPolicy{
							"policy-a": {},
						},
						BackendSets: map[string]loadbalancer.BackendSet{
							"backend-b": {},
							"backend-a": {},
						},
						Certificates: map[string]loadbalancer.Certificate{
							"cert-b": {},
							"cert-a": {},
						},
					},
				}, nil
			},
			deleteListener: func(
				_ context.Context,
				request loadbalancer.DeleteListenerRequest,
			) (loadbalancer.DeleteListenerResponse, error) {
				name := stringValue(request.ListenerName)
				operations = append(operations, "delete-listener:"+name)
				workRequestID := "wr-" + name
				return loadbalancer.DeleteListenerResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil
			},
			deleteRoutingPolicy: func(
				_ context.Context,
				request loadbalancer.DeleteRoutingPolicyRequest,
			) (loadbalancer.DeleteRoutingPolicyResponse, error) {
				name := stringValue(request.RoutingPolicyName)
				operations = append(operations, "delete-routing-policy:"+name)
				workRequestID := "wr-" + name
				return loadbalancer.DeleteRoutingPolicyResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil
			},
			deleteBackendSet: func(
				_ context.Context,
				request loadbalancer.DeleteBackendSetRequest,
			) (loadbalancer.DeleteBackendSetResponse, error) {
				name := stringValue(request.BackendSetName)
				operations = append(operations, "delete-backend-set:"+name)
				workRequestID := "wr-" + name
				return loadbalancer.DeleteBackendSetResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil
			},
			deleteCertificate: func(
				_ context.Context,
				request loadbalancer.DeleteCertificateRequest,
			) (loadbalancer.DeleteCertificateResponse, error) {
				name := stringValue(request.CertificateName)
				operations = append(operations, "delete-certificate:"+name)
				workRequestID := "wr-" + name
				return loadbalancer.DeleteCertificateResponse{
					OpcWorkRequestId: &workRequestID,
				}, nil
			},
			getWorkRequest: func(
				_ context.Context,
				request loadbalancer.GetWorkRequestRequest,
			) (loadbalancer.GetWorkRequestResponse, error) {
				id := stringValue(request.WorkRequestId)
				operations = append(operations, "wait:"+id)
				return loadbalancer.GetWorkRequestResponse{
					WorkRequest: loadbalancer.WorkRequest{
						LifecycleState: workRequests[id],
					},
				}, nil
			},
		}, logger, &LoadBalancerCleanerOptions{
			WorkRequestPollInterval: time.Millisecond,
			WorkRequestWaitTimeout:  50 * time.Millisecond,
		})

		result, err := cleaner.Cleanup(t.Context(), "ocid1.loadbalancer.oc1..cleanup")
		assertNoError(t, err)
		assertEqual(t, "203.0.113.50", result.PublicIP)
		assertSliceEqual(t, []string{"listener-a", "listener-b"}, result.DeletedListeners)
		assertSliceEqual(t, []string{"policy-a"}, result.DeletedRoutingPolicies)
		assertSliceEqual(t, []string{"backend-a", "backend-b"}, result.DeletedBackendSets)
		assertSliceEqual(t, []string{"cert-a", "cert-b"}, result.DeletedCertificates)
		assertSliceEqual(t, []string{
			"delete-listener:listener-a",
			"wait:wr-listener-a",
			"delete-listener:listener-b",
			"wait:wr-listener-b",
			"delete-routing-policy:policy-a",
			"wait:wr-policy-a",
			"delete-backend-set:backend-a",
			"wait:wr-backend-a",
			"delete-backend-set:backend-b",
			"wait:wr-backend-b",
			"delete-certificate:cert-a",
			"wait:wr-cert-a",
			"delete-certificate:cert-b",
			"wait:wr-cert-b",
		}, operations)
	})
}

type fakeLoadBalancerClient struct {
	getLoadBalancer     func(context.Context, loadbalancer.GetLoadBalancerRequest) (loadbalancer.GetLoadBalancerResponse, error)
	getRoutingPolicy    func(context.Context, loadbalancer.GetRoutingPolicyRequest) (loadbalancer.GetRoutingPolicyResponse, error)
	deleteListener      func(context.Context, loadbalancer.DeleteListenerRequest) (loadbalancer.DeleteListenerResponse, error)
	deleteRoutingPolicy func(context.Context, loadbalancer.DeleteRoutingPolicyRequest) (loadbalancer.DeleteRoutingPolicyResponse, error)
	deleteBackendSet    func(context.Context, loadbalancer.DeleteBackendSetRequest) (loadbalancer.DeleteBackendSetResponse, error)
	deleteCertificate   func(context.Context, loadbalancer.DeleteCertificateRequest) (loadbalancer.DeleteCertificateResponse, error)
	getWorkRequest      func(context.Context, loadbalancer.GetWorkRequestRequest) (loadbalancer.GetWorkRequestResponse, error)
}

func (f *fakeLoadBalancerClient) GetLoadBalancer(
	ctx context.Context,
	request loadbalancer.GetLoadBalancerRequest,
) (loadbalancer.GetLoadBalancerResponse, error) {
	if f.getLoadBalancer == nil {
		return loadbalancer.GetLoadBalancerResponse{}, errors.New("unexpected GetLoadBalancer call")
	}

	return f.getLoadBalancer(ctx, request)
}

func (f *fakeLoadBalancerClient) GetRoutingPolicy(
	ctx context.Context,
	request loadbalancer.GetRoutingPolicyRequest,
) (loadbalancer.GetRoutingPolicyResponse, error) {
	if f.getRoutingPolicy == nil {
		return loadbalancer.GetRoutingPolicyResponse{}, errors.New("unexpected GetRoutingPolicy call")
	}

	return f.getRoutingPolicy(ctx, request)
}

func (f *fakeLoadBalancerClient) DeleteListener(
	ctx context.Context,
	request loadbalancer.DeleteListenerRequest,
) (loadbalancer.DeleteListenerResponse, error) {
	if f.deleteListener == nil {
		return loadbalancer.DeleteListenerResponse{}, errors.New("unexpected DeleteListener call")
	}

	return f.deleteListener(ctx, request)
}

func (f *fakeLoadBalancerClient) DeleteRoutingPolicy(
	ctx context.Context,
	request loadbalancer.DeleteRoutingPolicyRequest,
) (loadbalancer.DeleteRoutingPolicyResponse, error) {
	if f.deleteRoutingPolicy == nil {
		return loadbalancer.DeleteRoutingPolicyResponse{}, errors.New("unexpected DeleteRoutingPolicy call")
	}

	return f.deleteRoutingPolicy(ctx, request)
}

func (f *fakeLoadBalancerClient) DeleteBackendSet(
	ctx context.Context,
	request loadbalancer.DeleteBackendSetRequest,
) (loadbalancer.DeleteBackendSetResponse, error) {
	if f.deleteBackendSet == nil {
		return loadbalancer.DeleteBackendSetResponse{}, errors.New("unexpected DeleteBackendSet call")
	}

	return f.deleteBackendSet(ctx, request)
}

func (f *fakeLoadBalancerClient) DeleteCertificate(
	ctx context.Context,
	request loadbalancer.DeleteCertificateRequest,
) (loadbalancer.DeleteCertificateResponse, error) {
	if f.deleteCertificate == nil {
		return loadbalancer.DeleteCertificateResponse{}, errors.New("unexpected DeleteCertificate call")
	}

	return f.deleteCertificate(ctx, request)
}

func (f *fakeLoadBalancerClient) GetWorkRequest(
	ctx context.Context,
	request loadbalancer.GetWorkRequestRequest,
) (loadbalancer.GetWorkRequestResponse, error) {
	if f.getWorkRequest == nil {
		return loadbalancer.GetWorkRequestResponse{}, errors.New("unexpected GetWorkRequest call")
	}

	return f.getWorkRequest(ctx, request)
}

func assertEqual[T comparable](t *testing.T, want T, got T) {
	t.Helper()

	if got != want {
		t.Fatalf("expected %v, got %v", want, got)
	}
}

func assertSliceEqual(t *testing.T, want []string, got []string) {
	t.Helper()

	if len(want) != len(got) {
		t.Fatalf("expected %v, got %v", want, got)
	}

	for idx := range want {
		if want[idx] != got[idx] {
			t.Fatalf("expected %v, got %v", want, got)
		}
	}
}

func assertNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
}

func assertErrorContains(t *testing.T, err error, fragment string) {
	t.Helper()

	if err == nil {
		t.Fatalf("expected error containing %q, got nil", fragment)
	}

	if !strings.Contains(err.Error(), fragment) {
		t.Fatalf("expected error containing %q, got %q", fragment, err.Error())
	}
}

func assertTrue(t *testing.T, value bool) {
	t.Helper()

	if !value {
		t.Fatal("expected true, got false")
	}
}

func makeIPAddress(ip string, isPublic bool) loadbalancer.IpAddress {
	ipValue := ip
	isPublicValue := isPublic

	return loadbalancer.IpAddress{
		IpAddress: &ipValue,
		IsPublic:  &isPublicValue,
	}
}

func TestWaitForRoutingPolicyRuleNamesAbsent(t *testing.T) {
	t.Parallel()

	t.Run("waits until the captured rules are gone", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		client := &fakeLoadBalancerClient{
			getRoutingPolicy: func(
				_ context.Context,
				request loadbalancer.GetRoutingPolicyRequest,
			) (loadbalancer.GetRoutingPolicyResponse, error) {
				callCount++
				assertEqual(t, "ocid1.loadbalancer.oc1..rules", stringValue(request.LoadBalancerId))
				assertEqual(t, "http_policy", stringValue(request.RoutingPolicyName))

				ruleNames := []string{"other-rule"}
				if callCount == 1 {
					ruleNames = append(ruleNames, "captured-rule")
				}

				rules := make([]loadbalancer.RoutingRule, 0, len(ruleNames))
				for _, ruleName := range ruleNames {
					name := ruleName
					rules = append(rules, loadbalancer.RoutingRule{Name: &name})
				}

				return loadbalancer.GetRoutingPolicyResponse{
					RoutingPolicy: loadbalancer.RoutingPolicy{
						Rules: rules,
					},
				}, nil
			},
		}

		err := WaitForRoutingPolicyRuleNamesAbsent(
			t.Context(),
			client,
			"ocid1.loadbalancer.oc1..rules",
			"http",
			[]string{"captured-rule"},
			&RoutingPolicyWaitOptions{PollInterval: time.Millisecond},
		)
		assertNoError(t, err)
		assertEqual(t, 2, callCount)
	})

	t.Run("rejects empty rule names", func(t *testing.T) {
		t.Parallel()

		err := WaitForRoutingPolicyRuleNamesAbsent(
			t.Context(),
			&fakeLoadBalancerClient{},
			"ocid1.loadbalancer.oc1..rules",
			"http",
			nil,
			nil,
		)
		assertErrorContains(t, err, "at least one rule name is required")
	})
}

func TestWaitForRoutingPolicyRuleNamesPresent(t *testing.T) {
	t.Parallel()

	t.Run("waits until the captured rules are present", func(t *testing.T) {
		t.Parallel()

		callCount := 0
		client := &fakeLoadBalancerClient{
			getRoutingPolicy: func(
				_ context.Context,
				request loadbalancer.GetRoutingPolicyRequest,
			) (loadbalancer.GetRoutingPolicyResponse, error) {
				callCount++
				assertEqual(t, "ocid1.loadbalancer.oc1..rules", stringValue(request.LoadBalancerId))
				assertEqual(t, "http_policy", stringValue(request.RoutingPolicyName))

				ruleNames := []string{"other-rule"}
				if callCount > 1 {
					ruleNames = append(ruleNames, "captured-rule")
				}

				rules := make([]loadbalancer.RoutingRule, 0, len(ruleNames))
				for _, ruleName := range ruleNames {
					name := ruleName
					rules = append(rules, loadbalancer.RoutingRule{Name: &name})
				}

				return loadbalancer.GetRoutingPolicyResponse{
					RoutingPolicy: loadbalancer.RoutingPolicy{
						Rules: rules,
					},
				}, nil
			},
		}

		err := WaitForRoutingPolicyRuleNamesPresent(
			t.Context(),
			client,
			"ocid1.loadbalancer.oc1..rules",
			"http",
			[]string{"captured-rule"},
			&RoutingPolicyWaitOptions{PollInterval: time.Millisecond},
		)
		assertNoError(t, err)
		assertEqual(t, 2, callCount)
	})

	t.Run("rejects empty rule names", func(t *testing.T) {
		t.Parallel()

		err := WaitForRoutingPolicyRuleNamesPresent(
			t.Context(),
			&fakeLoadBalancerClient{},
			"ocid1.loadbalancer.oc1..rules",
			"http",
			nil,
			nil,
		)
		assertErrorContains(t, err, "at least one rule name is required")
	})
}
