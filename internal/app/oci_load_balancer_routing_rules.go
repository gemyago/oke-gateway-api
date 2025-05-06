package app

import (
	"errors"
	"fmt"
	"strings"

	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var errUnsupportedMatch = errors.New("unsupported match type")

type ociLoadBalancerRoutingRulesMapper interface {
	// mapHTTPRouteMatchToCondition translates a Gateway API HTTPRouteMatch
	// into an OCI Load Balancer condition string.
	// Returns an empty string if the match is nil or empty.
	// Returns errUnsupportedMatch if any part of the match uses features
	// not supported by OCI Load Balancer rules (e.g., regex, query params, method).
	mapHTTPRouteMatchToCondition(match gatewayv1.HTTPRouteMatch) (string, error)
}

type ociLoadBalancerRoutingRulesMapperImpl struct{}

func newOciLoadBalancerRoutingRulesMapper() ociLoadBalancerRoutingRulesMapper {
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
		if match.Path.Value == nil {
			return "", errors.New("path match value cannot be nil")
		}
		pathValue := *match.Path.Value
		pathType := gatewayv1.PathMatchPathPrefix // Default type if not specified
		if match.Path.Type != nil {
			pathType = *match.Path.Type
		}

		switch pathType {
		case gatewayv1.PathMatchExact:
			// TODO: Handle escaping single quotes in pathValue if necessary
			conditions = append(conditions, fmt.Sprintf(`http.request.url.path eq '%s'`, pathValue))
		case gatewayv1.PathMatchPathPrefix:
			// TODO: Handle escaping single quotes in pathValue if necessary
			conditions = append(conditions, fmt.Sprintf(`http.request.url.path sw '%s'`, pathValue))
		case gatewayv1.PathMatchRegularExpression:
			return "", fmt.Errorf("%w: regex path matching", errUnsupportedMatch)
		default:
			return "", fmt.Errorf("%w: unknown path match type '%s'", errUnsupportedMatch, pathType)
		}
	}

	// --- Header Matching ---
	for _, headerMatch := range match.Headers {
		headerType := gatewayv1.HeaderMatchExact // Default type
		if headerMatch.Type != nil {
			headerType = *headerMatch.Type
		}

		switch headerType {
		case gatewayv1.HeaderMatchExact:
			// TODO: Handle escaping single quotes in headerMatch.Value if necessary
			// Header names are case-insensitive in HTTP, but OCI conditions might be case-sensitive.
			// Assuming case-sensitive match for now based on Gateway API spec.
			conditions = append(
				conditions,
				fmt.Sprintf(`http.request.headers['%s'] eq '%s'`, headerMatch.Name, headerMatch.Value),
			)
		case gatewayv1.HeaderMatchRegularExpression:
			return "", fmt.Errorf("%w: regex header matching for header '%s'", errUnsupportedMatch, headerMatch.Name)
		default:
			return "", fmt.Errorf("%w: unknown header match type '%s' for header '%s'",
				errUnsupportedMatch,
				headerType,
				headerMatch.Name,
			)
		}
	}

	// --- Combine conditions ---
	return strings.Join(conditions, " and "), nil
}
