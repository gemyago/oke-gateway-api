package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/samber/lo"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

var errUnsupportedMatch = errors.New("unsupported match type")

const conditionArgumentSeparatorLength = len(", ")

// Returns the prefix and true if it matches, empty string and false otherwise.
func parseRegexForStartsWith(pattern string) (string, bool) {
	value, ok := strings.CutPrefix(pattern, "^")
	if !ok {
		return "", false
	}

	value = strings.TrimSuffix(value, ".*$")
	value = strings.TrimSuffix(value, ".*")
	return parseRegexLiteral(value)
}

// Returns the suffix and true if it matches, empty string and false otherwise.
func parseRegexForEndsWith(pattern string) (string, bool) {
	value, ok := strings.CutSuffix(pattern, "$")
	if !ok {
		return "", false
	}

	value = strings.TrimPrefix(value, ".*")
	return parseRegexLiteral(value)
}

func parseRegexLiteral(value string) (string, bool) {
	if value == "" {
		return "", false
	}

	var literal strings.Builder
	for index := 0; index < len(value); index++ {
		if value[index] != '\\' {
			if !isOCIConditionLiteralByte(value[index]) {
				return "", false
			}
			if isRegexMetaCharacter(value[index]) {
				return "", false
			}
			literal.WriteByte(value[index])
			continue
		}

		index++
		if index >= len(value) {
			return "", false
		}
		switch value[index] {
		case '.', '/', '\\':
			if !isOCIConditionLiteralByte(value[index]) {
				return "", false
			}
			literal.WriteByte(value[index])
		default:
			return "", false
		}
	}

	return literal.String(), true
}

func isRegexMetaCharacter(value byte) bool {
	switch value {
	case '.', '^', '$', '*', '+', '?', '(', ')', '[', ']', '{', '}', '|':
		return true
	default:
		return false
	}
}

func isOCIConditionLiteralByte(value byte) bool {
	return value >= 0x20 && value != 0x7f && value != '\''
}

func validateOCIConditionLiteral(valueKind string, value string) error {
	for index := range len(value) {
		if !isOCIConditionLiteralByte(value[index]) {
			return fmt.Errorf(
				"%w: %s contains characters unsupported by OCI routing policy conditions",
				errUnsupportedMatch,
				valueKind,
			)
		}
	}
	return nil
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
		listenerPort gatewayv1.PortNumber,
		matches []gatewayv1.HTTPRouteMatch,
	) (string, error)

	// mapGRPCRouteHostnamesAndMatchesToCondition translates Gateway API hostnames and gRPC matches
	// to an OCI Load Balancer routing policy condition.
	mapGRPCRouteHostnamesAndMatchesToCondition(
		hostnames []gatewayv1.Hostname,
		listenerPort gatewayv1.PortNumber,
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
		if err := validateOCIConditionLiteral("path match value", pathValue); err != nil {
			return "", err
		}
		return fmt.Sprintf(`http.request.url.path eq '%s'`, pathValue), nil
	case gatewayv1.PathMatchPathPrefix:
		if err := validateOCIConditionLiteral("path match value", pathValue); err != nil {
			return "", err
		}
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
		if err := validateOCIConditionLiteral("header match name", string(headerMatch.Name)); err != nil {
			return "", err
		}
		if err := validateOCIConditionLiteral("header match value", headerMatch.Value); err != nil {
			return "", err
		}
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
	return mapRegexHeaderValueToCondition(string(headerMatch.Name), headerMatch.Value)
}

func mapRegexHeaderValueToCondition(headerName string, headerValue string) (string, error) {
	if err := validateOCIConditionLiteral("header match name", headerName); err != nil {
		return "", err
	}
	if prefix, swMatched := parseRegexForStartsWith(headerValue); swMatched {
		return fmt.Sprintf(`http.request.headers[(i '%s')][0] sw (i '%s')`, headerName, prefix), nil
	}
	if suffix, ewMatched := parseRegexForEndsWith(headerValue); ewMatched {
		return fmt.Sprintf(`http.request.headers[(i '%s')][0] ew (i '%s')`, headerName, suffix), nil
	}
	return "", fmt.Errorf(
		"%w: regex header matching for header '%s'",
		errUnsupportedMatch,
		headerName,
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
	listenerPort gatewayv1.PortNumber,
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
		for _, hostCondition := range hostConditionsForHostname(hostname, listenerPort) {
			if len(matchConditions) == 0 {
				conditions = append(conditions, hostCondition)
				continue
			}
			for _, matchCondition := range matchConditions {
				conditions = append(conditions, allRoutingConditions(hostCondition, matchCondition))
			}
		}
	}

	return fmt.Sprintf("any(%s)", strings.Join(conditions, ", ")), nil
}

func (r *ociLoadBalancerRoutingRulesMapperImpl) mapGRPCRouteHostnamesAndMatchesToCondition(
	hostnames []gatewayv1.Hostname,
	listenerPort gatewayv1.PortNumber,
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
		for _, hostCondition := range hostConditionsForHostname(hostname, listenerPort) {
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
	}

	return fmt.Sprintf("any(%s)", strings.Join(conditions, ", ")), nil
}

func hostConditionsForHostname(hostname gatewayv1.Hostname, listenerPort gatewayv1.PortNumber) []string {
	hostnameValue := string(hostname)
	conditions := []string{hostHeaderEquals(hostnameValue)}
	if listenerPort > 0 && !strings.HasPrefix(hostnameValue, "*.") && !strings.Contains(hostnameValue, ":") {
		conditions = append(conditions, hostHeaderEquals(fmt.Sprintf("%s:%d", hostnameValue, listenerPort)))
	}
	return conditions
}

func hostHeaderEquals(hostname string) string {
	return fmt.Sprintf(`http.request.headers[(i 'host')] eq (i '%s')`, hostname)
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
	filteredConditions := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		filteredConditions = appendRoutingConditionParts(filteredConditions, condition)
	}
	if len(filteredConditions) == 0 {
		return ""
	}
	if len(filteredConditions) == 1 {
		return filteredConditions[0]
	}
	return "all(" + strings.Join(filteredConditions, ", ") + ")"
}

func appendRoutingConditionParts(conditions []string, condition string) []string {
	if condition == "" {
		return conditions
	}
	inner, ok := strings.CutPrefix(condition, "all(")
	if !ok {
		return append(conditions, condition)
	}
	inner, ok = strings.CutSuffix(inner, ")")
	if !ok {
		return append(conditions, condition)
	}
	parts, ok := splitOCIConditionArguments(inner)
	if !ok {
		return append(conditions, condition)
	}
	return append(conditions, parts...)
}

func splitOCIConditionArguments(value string) ([]string, bool) {
	if value == "" {
		return nil, false
	}

	var parts []string
	start := 0
	depth := 0
	inLiteral := false
	for index := 0; index < len(value); index++ {
		char := value[index]
		if char == '\'' {
			inLiteral = !inLiteral
			continue
		}
		if inLiteral {
			continue
		}

		nextDepth, ok := updateOCIConditionDepth(depth, char)
		if !ok {
			return nil, false
		}
		depth = nextDepth
		if char != ',' || depth != 0 {
			continue
		}

		if !hasOCIConditionArgumentSeparator(value, index) {
			return nil, false
		}
		var part string
		part, ok = trimOCIConditionArgument(value[start:index])
		if !ok {
			return nil, false
		}
		parts = append(parts, part)
		start = index + conditionArgumentSeparatorLength
		index++
	}
	if inLiteral || depth != 0 {
		return nil, false
	}

	part, ok := trimOCIConditionArgument(value[start:])
	if !ok {
		return nil, false
	}
	parts = append(parts, part)
	return parts, true
}

func updateOCIConditionDepth(depth int, value byte) (int, bool) {
	switch value {
	case '(':
		return depth + 1, true
	case ')':
		depth--
		return depth, depth >= 0
	default:
		return depth, true
	}
}

func hasOCIConditionArgumentSeparator(value string, index int) bool {
	return index+1 < len(value) && value[index+1] == ' '
}

func trimOCIConditionArgument(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", false
	}
	return value, true
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
	if err := validateOCIConditionLiteral("grpc service match value", service); err != nil {
		return "", err
	}
	if err := validateOCIConditionLiteral("grpc method match value", method); err != nil {
		return "", err
	}
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

	switch headerType {
	case gatewayv1.GRPCHeaderMatchExact:
		if err := validateOCIConditionLiteral("grpc header match name", string(headerMatch.Name)); err != nil {
			return "", err
		}
		if err := validateOCIConditionLiteral("grpc header match value", headerMatch.Value); err != nil {
			return "", err
		}
		return fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerMatch.Name, headerMatch.Value), nil
	case gatewayv1.GRPCHeaderMatchRegularExpression:
		return mapRegexHeaderValueToCondition(string(headerMatch.Name), headerMatch.Value)
	default:
		return "", fmt.Errorf(
			"%w: unsupported grpc header match type '%s' for header '%s'",
			errUnsupportedMatch,
			headerType,
			headerMatch.Name,
		)
	}
}
