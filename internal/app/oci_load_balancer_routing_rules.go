package app

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/samber/lo"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var errUnsupportedMatch = errors.New("unsupported match type")

// Allow alphanumeric characters, hyphens, underscores, and escaped dots.
var startsWithExpression = regexp.MustCompile(`^\^([a-zA-Z0-9\-_\\\.]+?)(?:\.\*)?$`)

// Allow alphanumeric characters, hyphens, underscores, and escaped dots.
var endsWithExpression = regexp.MustCompile(`^([a-zA-Z0-9\-_\\\.]+?)\$$`)

const expectedMatchesLength = 2

// Returns the prefix and true if it matches, empty string and false otherwise.
func parseRegexForStartsWith(pattern string) (string, bool) {
	matches := startsWithExpression.FindStringSubmatch(pattern)
	if len(matches) != expectedMatchesLength {
		return "", false
	}

	// Unescape dots in the prefix
	prefix := strings.ReplaceAll(matches[1], "\\.", ".")
	return prefix, true
}

// Returns the suffix and true if it matches, empty string and false otherwise.
func parseRegexForEndsWith(pattern string) (string, bool) {
	matches := endsWithExpression.FindStringSubmatch(pattern)
	if len(matches) != expectedMatchesLength {
		return "", false
	}

	// Unescape dots in the suffix
	suffix := strings.ReplaceAll(matches[1], "\\.", ".")
	return suffix, true
}

type ociLoadBalancerRoutingRulesMapper interface {
	// mapHTTPRouteMatchToCondition translates a Gateway API HTTPRouteMatch
	// into an OCI Load Balancer condition string.
	// Returns an empty string if the match is nil or empty.
	// Returns errUnsupportedMatch if any part of the match uses features
	// not supported by OCI Load Balancer rules (e.g., regex, query params, method).
	mapHTTPRouteMatchToCondition(match gatewayv1.HTTPRouteMatch) (string, error)

	// mapHTTPRouteMatchesToCondition translates a Gateway API HTTPRouteMatches
	// to a list of OCI Load Balancer conditions as a string..
	mapHTTPRouteMatchesToCondition(matches []gatewayv1.HTTPRouteMatch) (string, error)

	// mapHTTPRouteHostnamesAndMatchesToCondition translates Gateway API hostnames and matches
	// to an OCI Load Balancer routing policy condition.
	mapHTTPRouteHostnamesAndMatchesToCondition(
		hostnames []gatewayv1.Hostname,
		matches []gatewayv1.HTTPRouteMatch,
	) (string, error)

	// mapGRPCRouteHostnamesAndMatchesToCondition translates Gateway API hostnames and gRPC matches
	// to an OCI Load Balancer routing policy condition.
	mapGRPCRouteHostnamesAndMatchesToCondition(
		hostnames []gatewayv1.Hostname,
		matches []gatewayv1.GRPCRouteMatch,
	) (string, error)
}

type ociLoadBalancerRoutingRulesMapperImpl struct{}

func newOciLoadBalancerRoutingRulesMapper() *ociLoadBalancerRoutingRulesMapperImpl {
	return &ociLoadBalancerRoutingRulesMapperImpl{}
}

// mapHTTPRouteMatchToCondition translates Gateway API match rules into OCI condition strings.
func (r *ociLoadBalancerRoutingRulesMapperImpl) mapHTTPRouteMatchToCondition(
	match gatewayv1.HTTPRouteMatch,
) (string, error) {
	var conditions []string

	// --- Unsupported Checks First ---
	if len(match.QueryParams) > 0 {
		return "", fmt.Errorf("%w: query parameter matching", errUnsupportedMatch)
	}
	if match.Method != nil {
		return "", fmt.Errorf("%w: method matching", errUnsupportedMatch)
	}

	// --- Path Matching ---
	if match.Path != nil {
		condition, err := mapPathMatchToCondition(*match.Path)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, condition)
	}

	// --- Header Matching ---
	for _, headerMatch := range match.Headers {
		condition, err := mapHeaderMatchToCondition(headerMatch)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, condition)
	}

	// --- Combine conditions ---
	if len(conditions) == 0 {
		return "", nil
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return "all(" + strings.Join(conditions, ", ") + ")", nil
}

func mapPathMatchToCondition(pathMatch gatewayv1.HTTPPathMatch) (string, error) {
	if pathMatch.Value == nil {
		return "", errors.New("path match value cannot be nil")
	}
	pathValue := *pathMatch.Value
	pathType := gatewayv1.PathMatchPathPrefix // Default type if not specified
	if pathMatch.Type != nil {
		pathType = *pathMatch.Type
	}

	switch pathType {
	case gatewayv1.PathMatchExact:
		// TODO: Handle escaping single quotes in pathValue if necessary
		return fmt.Sprintf(`http.request.url.path eq '%s'`, pathValue), nil
	case gatewayv1.PathMatchPathPrefix:
		// TODO: Handle escaping single quotes in pathValue if necessary
		return fmt.Sprintf(`http.request.url.path sw '%s'`, pathValue), nil
	case gatewayv1.PathMatchRegularExpression:
		return "", fmt.Errorf("%w: regex path matching", errUnsupportedMatch)
	default:
		return "", fmt.Errorf("%w: unknown path match type '%s'", errUnsupportedMatch, pathType)
	}
}

func mapHeaderMatchToCondition(headerMatch gatewayv1.HTTPHeaderMatch) (string, error) {
	headerType := gatewayv1.HeaderMatchExact // Default type
	if headerMatch.Type != nil {
		headerType = *headerMatch.Type
	}

	switch headerType {
	case gatewayv1.HeaderMatchExact:
		// TODO: Handle escaping single quotes in headerMatch.Value if necessary
		// Header names are case-insensitive in HTTP, but OCI conditions might be case-sensitive.
		// Assuming case-sensitive match for now based on Gateway API spec.
		return fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerMatch.Name, headerMatch.Value), nil
	case gatewayv1.HeaderMatchRegularExpression:
		return mapRegexHeaderMatchToCondition(headerMatch)
	default:
		return "", fmt.Errorf("%w: unknown header match type '%s' for header '%s'",
			errUnsupportedMatch,
			headerType,
			headerMatch.Name,
		)
	}
}

func mapRegexHeaderMatchToCondition(headerMatch gatewayv1.HTTPHeaderMatch) (string, error) {
	if prefix, swMatched := parseRegexForStartsWith(headerMatch.Value); swMatched {
		return fmt.Sprintf(`http.request.headers[(i '%s')][0] sw (i '%s')`, headerMatch.Name, prefix), nil
	}
	if suffix, ewMatched := parseRegexForEndsWith(headerMatch.Value); ewMatched {
		return fmt.Sprintf(`http.request.headers[(i '%s')][0] ew (i '%s')`, headerMatch.Name, suffix), nil
	}
	return "", fmt.Errorf(
		"%w: regex header matching for header '%s'",
		errUnsupportedMatch,
		headerMatch.Name,
	)
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapHTTPRouteMatchesToCondition(
	matches []gatewayv1.HTTPRouteMatch,
) (string, error) {
	conditions, err := r.mapHTTPRouteMatchesToConditions(matches)
	if err != nil {
		return "", err
	}
	if len(conditions) == 0 {
		return "", nil
	}

	return fmt.Sprintf("any(%s)", strings.Join(conditions, ", ")), nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapHTTPRouteMatchesToConditions(
	matches []gatewayv1.HTTPRouteMatch,
) ([]string, error) {
	if len(matches) == 0 {
		return nil, nil
	}

	var conditions []string
	for _, match := range matches {
		condition, err := r.mapHTTPRouteMatchToCondition(match)
		if err != nil {
			return nil, err // Propagate error if any single match fails
		}
		if condition != "" {
			conditions = append(conditions, condition)
		}
	}

	return conditions, nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapHTTPRouteHostnamesAndMatchesToCondition(
	hostnames []gatewayv1.Hostname,
	matches []gatewayv1.HTTPRouteMatch,
) (string, error) {
	if len(hostnames) == 0 {
		return r.mapHTTPRouteMatchesToCondition(matches)
	}

	matchConditions, err := r.mapHTTPRouteMatchesToConditions(matches)
	if err != nil {
		return "", err
	}

	conditions := make([]string, 0, len(hostnames)*max(1, len(matchConditions)))
	for _, hostname := range hostnames {
		hostCondition := fmt.Sprintf(`http.request.headers[(i 'host')] eq (i '%s')`, hostname)
		if len(matchConditions) == 0 {
			conditions = append(conditions, hostCondition)
			continue
		}
		for _, matchCondition := range matchConditions {
			conditions = append(conditions, "all("+hostCondition+", "+matchCondition+")")
		}
	}

	return fmt.Sprintf("any(%s)", strings.Join(conditions, ", ")), nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapGRPCRouteHostnamesAndMatchesToCondition(
	hostnames []gatewayv1.Hostname,
	matches []gatewayv1.GRPCRouteMatch,
) (string, error) {
	if len(hostnames) == 0 {
		return r.mapGRPCRouteMatchesToCondition(matches)
	}

	matchConditions, err := r.mapGRPCRouteMatchesToConditions(matches)
	if err != nil {
		return "", err
	}

	conditions := make([]string, 0, len(hostnames)*max(1, len(matchConditions))*len(grpcContentTypeConditions()))
	for _, hostname := range hostnames {
		hostCondition := fmt.Sprintf(`http.request.headers[(i 'host')] eq (i '%s')`, hostname)
		if len(matchConditions) == 0 {
			for _, contentTypeCondition := range grpcContentTypeConditions() {
				conditions = append(conditions, allRoutingConditions(hostCondition, contentTypeCondition))
			}
			continue
		}
		for _, matchCondition := range matchConditions {
			for _, contentTypeCondition := range grpcContentTypeConditions() {
				conditions = append(
					conditions,
					allRoutingConditions(hostCondition, contentTypeCondition, matchCondition),
				)
			}
		}
	}

	return fmt.Sprintf("any(%s)", strings.Join(conditions, ", ")), nil
}

func grpcContentTypeCondition() string {
	return "any(" + strings.Join(grpcContentTypeConditions(), ", ") + ")"
}

func grpcContentTypeConditions() []string {
	return []string{
		"http.request.headers[(i 'content-type')][0] eq (i 'application/grpc')",
		"http.request.headers[(i 'content-type')][0] sw (i 'application/grpc+')",
		"http.request.headers[(i 'content-type')][0] sw (i 'application/grpc;')",
	}
}

func allRoutingConditions(conditions ...string) string {
	filteredConditions := lo.Filter(conditions, func(condition string, _ int) bool {
		return condition != ""
	})
	if len(filteredConditions) == 0 {
		return ""
	}
	if len(filteredConditions) == 1 {
		return filteredConditions[0]
	}
	return "all(" + strings.Join(filteredConditions, ", ") + ")"
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapGRPCRouteMatchesToCondition(
	matches []gatewayv1.GRPCRouteMatch,
) (string, error) {
	conditions, err := r.mapGRPCRouteMatchesToConditions(matches)
	if err != nil {
		return "", err
	}
	if len(conditions) == 0 {
		return grpcContentTypeCondition(), nil
	}

	grpcConditions := make([]string, 0, len(conditions)*len(grpcContentTypeConditions()))
	for _, condition := range conditions {
		for _, contentTypeCondition := range grpcContentTypeConditions() {
			grpcConditions = append(grpcConditions, allRoutingConditions(contentTypeCondition, condition))
		}
	}

	return fmt.Sprintf("any(%s)", strings.Join(grpcConditions, ", ")), nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapGRPCRouteMatchesToConditions(
	matches []gatewayv1.GRPCRouteMatch,
) ([]string, error) {
	if len(matches) == 0 {
		return nil, nil
	}

	conditions := make([]string, 0, len(matches))
	for _, match := range matches {
		condition, err := r.mapGRPCRouteMatchToCondition(match)
		if err != nil {
			return nil, err
		}
		if condition != "" {
			conditions = append(conditions, condition)
		}
	}

	return conditions, nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapGRPCRouteMatchToCondition(
	match gatewayv1.GRPCRouteMatch,
) (string, error) {
	conditions := make([]string, 0, 1+len(match.Headers))

	if match.Method != nil {
		methodCondition, err := mapGRPCMethodMatchToCondition(*match.Method)
		if err != nil {
			return "", err
		}
		if methodCondition != "" {
			conditions = append(conditions, methodCondition)
		}
	}

	for _, headerMatch := range match.Headers {
		condition, err := mapGRPCHeaderMatchToCondition(headerMatch)
		if err != nil {
			return "", err
		}
		conditions = append(conditions, condition)
	}

	if len(conditions) == 0 {
		return "", nil
	}
	if len(conditions) == 1 {
		return conditions[0], nil
	}
	return "all(" + strings.Join(conditions, ", ") + ")", nil
}

func mapGRPCMethodMatchToCondition(methodMatch gatewayv1.GRPCMethodMatch) (string, error) {
	matchType := gatewayv1.GRPCMethodMatchExact
	if methodMatch.Type != nil {
		matchType = *methodMatch.Type
	}
	if matchType != gatewayv1.GRPCMethodMatchExact {
		return "", fmt.Errorf("%w: grpc regex method matching", errUnsupportedMatch)
	}

	service := strings.TrimPrefix(lo.FromPtr(methodMatch.Service), ".")
	method := lo.FromPtr(methodMatch.Method)
	switch {
	case service != "" && method != "":
		return fmt.Sprintf(`http.request.url.path eq '/%s/%s'`, service, method), nil
	case service != "":
		return fmt.Sprintf(`http.request.url.path sw '/%s/'`, service), nil
	case method != "":
		return fmt.Sprintf(`http.request.url.path ew '/%s'`, method), nil
	default:
		return "", errors.New("grpc method match requires service or method")
	}
}

func mapGRPCHeaderMatchToCondition(headerMatch gatewayv1.GRPCHeaderMatch) (string, error) {
	headerType := gatewayv1.GRPCHeaderMatchExact
	if headerMatch.Type != nil {
		headerType = *headerMatch.Type
	}
	if headerType != gatewayv1.GRPCHeaderMatchExact {
		return "", fmt.Errorf(
			"%w: unsupported grpc header match type '%s' for header '%s'",
			errUnsupportedMatch,
			headerType,
			headerMatch.Name,
		)
	}

	return fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerMatch.Name, headerMatch.Value), nil
}
