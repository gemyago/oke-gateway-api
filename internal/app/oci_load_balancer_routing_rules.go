package app

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

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
