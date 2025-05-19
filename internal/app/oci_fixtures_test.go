package app

import (
	"math/rand/v2"

	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
	"github.com/samber/lo"
)

type randomOCIBackendSetOpt func(*loadbalancer.BackendSet)

func makeRandomOCIBackendSet(
	opts ...randomOCIBackendSetOpt,
) loadbalancer.BackendSet {
	var knownPolicies = []string{
		"ROUND_ROBIN",
		"LEAST_CONNECTIONS",
		"IP_HASH",
		"STICKY_SESSION",
	}
	bs := loadbalancer.BackendSet{
		Name: lo.ToPtr(faker.DomainName()),
		HealthChecker: &loadbalancer.HealthChecker{
			Protocol:   lo.ToPtr("HTTP"),
			Port:       lo.ToPtr(rand.IntN(65535)),
			UrlPath:    lo.ToPtr("/" + faker.Word()),
			ReturnCode: lo.ToPtr(200),
		},
		Policy:                lo.ToPtr(knownPolicies[rand.IntN(len(knownPolicies))]),
		BackendMaxConnections: lo.ToPtr(rand.IntN(1000)),
		SslConfiguration: &loadbalancer.SslConfiguration{
			CertificateName: lo.ToPtr(faker.DomainName()),
		},
		SessionPersistenceConfiguration: &loadbalancer.SessionPersistenceConfigurationDetails{
			CookieName: lo.ToPtr(faker.DomainName()),
		},
		LbCookieSessionPersistenceConfiguration: &loadbalancer.LbCookieSessionPersistenceConfigurationDetails{
			CookieName: lo.ToPtr(faker.DomainName()),
		},
	}

	for _, opt := range opts {
		opt(&bs)
	}

	return bs
}

func randomOCIBackendSetWithNameOpt(name string) randomOCIBackendSetOpt {
	return func(bs *loadbalancer.BackendSet) {
		bs.Name = lo.ToPtr(name)
	}
}

func randomOCIBackendSetWithBackendsOpt(backends []loadbalancer.Backend) randomOCIBackendSetOpt {
	return func(bs *loadbalancer.BackendSet) {
		bs.Backends = backends
	}
}

func makeRandomOCIBackend() loadbalancer.Backend {
	return loadbalancer.Backend{
		Name:      lo.ToPtr(faker.DomainName()),
		Port:      lo.ToPtr(rand.IntN(65535)),
		IpAddress: lo.ToPtr(faker.IPv4()),
	}
}

func makeFewRandomOCIBackends() []loadbalancer.Backend {
	count := 2 + rand.IntN(3)
	backends := make([]loadbalancer.Backend, count)
	for i := range backends {
		backends[i] = makeRandomOCIBackend()
	}
	return backends
}

func makeRandomOCIBackendDetails() loadbalancer.BackendDetails {
	return loadbalancer.BackendDetails{
		Port:      lo.ToPtr(rand.IntN(65535)),
		IpAddress: lo.ToPtr(faker.IPv4()),
	}
}

func makeFewRandomOCIBackendDetails() []loadbalancer.BackendDetails {
	count := 2 + rand.IntN(3)
	backends := make([]loadbalancer.BackendDetails, count)
	for i := range backends {
		backends[i] = makeRandomOCIBackendDetails()
	}
	return backends
}

type randomOCIListenerOpt func(*loadbalancer.Listener)

func makeRandomOCIListener(
	opts ...randomOCIListenerOpt,
) loadbalancer.Listener {
	listener := loadbalancer.Listener{
		Name: lo.ToPtr(faker.DomainName()),
	}

	for _, opt := range opts {
		opt(&listener)
	}

	return listener
}

type randomOCILoadBalancerOpt func(*loadbalancer.LoadBalancer)

func makeRandomOCILoadBalancer(
	opts ...randomOCILoadBalancerOpt,
) loadbalancer.LoadBalancer {
	lb := loadbalancer.LoadBalancer{
		Id:        lo.ToPtr(faker.UUIDHyphenated()),
		Listeners: map[string]loadbalancer.Listener{},
	}

	for _, opt := range opts {
		opt(&lb)
	}

	return lb
}

func randomOCILoadBalancerWithRandomBackendSetsOpt() randomOCILoadBalancerOpt {
	return func(lb *loadbalancer.LoadBalancer) {
		lb.BackendSets = map[string]loadbalancer.BackendSet{}
		for range lb.BackendSets {
			bs := makeRandomOCIBackendSet()
			lb.BackendSets[*bs.Name] = bs
		}
	}
}

func randomOCILoadBalancerWithRandomPoliciesOpt() randomOCILoadBalancerOpt {
	return func(lb *loadbalancer.LoadBalancer) {
		lb.RoutingPolicies = map[string]loadbalancer.RoutingPolicy{}
		for range lb.RoutingPolicies {
			policy := makeRandomOCIRoutingPolicy()
			lb.RoutingPolicies[*policy.Name] = policy
		}
	}
}

func randomOCILoadBalancerWithRandomCertificatesOpt() randomOCILoadBalancerOpt {
	return func(lb *loadbalancer.LoadBalancer) {
		lb.Certificates = makeFewRandomOCICertificatesMap()
	}
}

type randomOCIRoutingPolicyOpt func(*loadbalancer.RoutingPolicy)

func makeRandomOCIRoutingPolicy(
	opts ...randomOCIRoutingPolicyOpt,
) loadbalancer.RoutingPolicy {
	policy := loadbalancer.RoutingPolicy{
		Name:                     lo.ToPtr(faker.DomainName()),
		ConditionLanguageVersion: loadbalancer.RoutingPolicyConditionLanguageVersionV1,
		Rules: []loadbalancer.RoutingRule{
			makeRandomOCIRoutingRule(),
			makeRandomOCIRoutingRule(),
		},
	}

	for _, opt := range opts {
		opt(&policy)
	}

	return policy
}

func makeRandomOCIRoutingRule() loadbalancer.RoutingRule {
	return loadbalancer.RoutingRule{
		Name: lo.ToPtr(faker.UUIDHyphenated() + "-rr." + faker.DomainName()),
	}
}

func makeRandomOCICertificate() loadbalancer.Certificate {
	return loadbalancer.Certificate{
		CertificateName:   lo.ToPtr(faker.DomainName()),
		PublicCertificate: lo.ToPtr(faker.UUIDHyphenated()),
		CaCertificate:     lo.ToPtr(faker.UUIDHyphenated()),
	}
}

func makeFewRandomOCICertificates() []loadbalancer.Certificate {
	count := 2 + rand.IntN(3)
	certificates := make([]loadbalancer.Certificate, count)
	for i := range certificates {
		certificates[i] = makeRandomOCICertificate()
	}
	return certificates
}

func makeFewRandomOCICertificatesMap() map[string]loadbalancer.Certificate {
	certificates := makeFewRandomOCICertificates()
	certificatesMap := make(map[string]loadbalancer.Certificate)
	for _, certificate := range certificates {
		certificatesMap[*certificate.CertificateName] = certificate
	}
	return certificatesMap
}
