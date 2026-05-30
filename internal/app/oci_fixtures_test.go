package app

import (
	"math/rand/v2"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/loadbalancer"
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
		Name: new(faker.New().Internet().Domain()),
		HealthChecker: &loadbalancer.HealthChecker{
			Protocol:   new("HTTP"),
			Port:       new(rand.IntN(65535)),
			UrlPath:    new("/" + faker.New().Lorem().Word()),
			ReturnCode: new(200),
		},
		Policy:                new(knownPolicies[rand.IntN(len(knownPolicies))]),
		BackendMaxConnections: new(rand.IntN(1000)),
		SslConfiguration: &loadbalancer.SslConfiguration{
			CertificateName: new(faker.New().Internet().Domain()),
		},
		SessionPersistenceConfiguration: &loadbalancer.SessionPersistenceConfigurationDetails{
			CookieName: new(faker.New().Internet().Domain()),
		},
		LbCookieSessionPersistenceConfiguration: &loadbalancer.LbCookieSessionPersistenceConfigurationDetails{
			CookieName: new(faker.New().Internet().Domain()),
		},
	}

	for _, opt := range opts {
		opt(&bs)
	}

	return bs
}

func randomOCIBackendSetWithNameOpt(name string) randomOCIBackendSetOpt {
	return func(bs *loadbalancer.BackendSet) {
		bs.Name = new(name)
	}
}

func randomOCIBackendSetWithBackendsOpt(backends []loadbalancer.Backend) randomOCIBackendSetOpt {
	return func(bs *loadbalancer.BackendSet) {
		bs.Backends = backends
	}
}

func makeRandomOCIBackend() loadbalancer.Backend {
	return loadbalancer.Backend{
		Name:      new(faker.New().Internet().Domain()),
		Port:      new(rand.IntN(65535)),
		IpAddress: new(faker.New().Internet().Ipv4()),
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
		Port:      new(rand.IntN(65535)),
		IpAddress: new(faker.New().Internet().Ipv4()),
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
		Name: new(faker.New().Internet().Domain()),
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
		Id:        new(faker.New().UUID().V4()),
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
		Name:                     new(faker.New().Internet().Domain()),
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
		Name: new(faker.New().UUID().V4() + "-rr." + faker.New().Internet().Domain()),
	}
}

func makeRandomOCICertificate() loadbalancer.Certificate {
	return loadbalancer.Certificate{
		CertificateName:   new(faker.New().Internet().Domain()),
		PublicCertificate: new(faker.New().UUID().V4()),
		CaCertificate:     new(faker.New().UUID().V4()),
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
