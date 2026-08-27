package app

import (
	"fmt"
	"strings"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/samber/lo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	gatewayv1 "sigs.k8s.io/gateway-api/apis/v1"
)

func TestOciLoadBalancerRoutingRulesMapper(t *testing.T) {
	t.Run("parseRegexLiteral", func(t *testing.T) {
		tests := []struct {
			name   string
			value  string
			want   string
			wantOK bool
		}{
			{
				name:   "rejects empty value",
				value:  "",
				wantOK: false,
			},
			{
				name:   "rejects dangling escape",
				value:  `api\`,
				wantOK: false,
			},
			{
				name:   "rejects unsupported escape",
				value:  `api\-`,
				wantOK: false,
			},
			{
				name:   "rejects regex metacharacter",
				value:  `api+`,
				wantOK: false,
			},
			{
				name:   "rejects single quote",
				value:  `api'admin`,
				wantOK: false,
			},
			{
				name:   "rejects control character",
				value:  "api\nadmin",
				wantOK: false,
			},
			{
				name:   "unescapes slash dot and backslash",
				value:  `v1\/api\.example\\internal`,
				want:   `v1/api.example\internal`,
				wantOK: true,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := parseRegexLiteral(tc.value)

				assert.Equal(t, tc.wantOK, ok)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("mapHTTPRouteMatchToCondition", func(t *testing.T) {
		type testCase struct {
			name        string
			match       gatewayv1.HTTPRouteMatch
			want        string
			wantErrIs   error
			wantErrText string
		}

		tests := []func() testCase{
			func() testCase {
				fake := faker.New()
				pathValue := "/" + fake.Lorem().Word() + "/" + fake.Lorem().Word()
				return testCase{
					name: "exact path match",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchExact),
							Value: new(pathValue),
						},
					},
					want: fmt.Sprintf(`http.request.url.path eq '%s'`, pathValue),
				}
			},
			func() testCase {
				fake := faker.New()
				pathPrefix := "/" + fake.Lorem().Word() + "/" + fake.Lorem().Word()
				return testCase{
					name: "prefix path match",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
							Value: new(pathPrefix),
						},
					},
					want: fmt.Sprintf(`http.request.url.path sw '%s'`, pathPrefix),
				}
			},
			func() testCase {
				fake := faker.New()
				pathValue := "/" + fake.Lorem().Word() + "'bad"
				return testCase{
					name: "rejects path value with single quote",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchExact),
							Value: &pathValue,
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				headerValue := fake.UUID().V4()
				return testCase{
					name: "exact header match",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: headerValue,
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerName, headerValue),
				}
			},
			func() testCase {
				fake := faker.New()
				return testCase{
					name: "rejects exact header name with single quote",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName("x-" + fake.Lorem().Word() + "'bad"),
								Value: fake.UUID().V4(),
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "rejects exact header value with control character",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "tenant\n" + fake.Lorem().Word(),
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName1 := "X-" + fake.Lorem().Word() + "-1"
				headerValue1 := fake.Lorem().Word()
				headerName2 := "X-" + fake.Lorem().Word() + "-2"
				headerValue2 := fake.UUID().V4()
				return testCase{
					name: "multiple exact header matches",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName(headerName1),
								Value: headerValue1,
							},
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName(headerName2),
								Value: headerValue2,
							},
						},
					},
					want: fmt.Sprintf(
						"all(%s, %s)",
						fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerName1, headerValue1),
						fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerName2, headerValue2),
					),
				}
			},
			func() testCase {
				fake := faker.New()
				pathValue := "/" + fake.Lorem().Word() + "/" + fake.Lorem().Word()
				headerName := "Content-Type"
				headerValue := "application/" + fake.Lorem().Word()
				return testCase{
					name: "exact path and exact header match",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchExact),
							Value: new(pathValue),
						},
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: headerValue,
							},
						},
					},
					want: fmt.Sprintf(
						"all(%s, %s)",
						fmt.Sprintf(`http.request.url.path eq '%s'`, pathValue),
						fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerName, headerValue),
					),
				}
			},
			func() testCase {
				fake := faker.New()
				authValue := "Bearer " + fake.UUID().V4()
				requestID := fake.UUID().V4()
				return testCase{
					name: "prefix path and multiple exact header matches",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
							Value: new("/api/v1"),
						},
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  "Authorization",
								Value: authValue,
							},
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchExact),
								Name:  "X-Request-ID",
								Value: requestID,
							},
						},
					},
					want: fmt.Sprintf(
						"all(%s, %s, %s)",
						`http.request.url.path sw '/api/v1'`,
						fmt.Sprintf(`http.request.headers[(i 'Authorization')] eq (i '%s')`, authValue),
						fmt.Sprintf(`http.request.headers[(i 'X-Request-ID')] eq (i '%s')`, requestID),
					),
				}
			},
			func() testCase {
				return testCase{
					name: "unsupported path type regex",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchRegularExpression),
							Value: new("/users/[0-9]+"),
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				unknownPathType := gatewayv1.PathMatchType("Unknown")
				return testCase{
					name: "unsupported path type unknown",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  &unknownPathType,
							Value: new("/unknown"),
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				return testCase{
					name: "unsupported header type regex",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  "X-User-ID",
								Value: "^[a-z]+$",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				unknownHeaderType := gatewayv1.HeaderMatchType("Unknown")
				return testCase{
					name: "unsupported header type unknown",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  &unknownHeaderType,
								Name:  "X-User-ID",
								Value: "123",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - starts with simple prefix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^foo",
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')][0] sw (i 'foo')`, headerName),
				}
			},
			func() testCase {
				headerName := "Content-Type"
				return testCase{
					name: "regex header match - starts with dotted prefix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^foo\\.bar",
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')][0] sw (i 'foo.bar')`, headerName),
				}
			},
			func() testCase {
				headerName := "Authorization"
				return testCase{
					name: "regex header match - starts with complex prefix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^foo\\.bar\\.baz.*",
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')][0] sw (i 'foo.bar.baz')`, headerName),
				}
			},
			func() testCase {
				return testCase{
					name: "regex header match - starts with host prefix from issue",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  "Host",
								Value: `^community-manager-api\..*$`,
							},
						},
					},
					want: `http.request.headers[(i 'Host')][0] sw (i 'community-manager-api.')`,
				}
			},
			func() testCase {
				return testCase{
					name: "regex header match - starts with slash prefix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  "X-API-Version",
								Value: `^v1\/`,
							},
						},
					},
					want: `http.request.headers[(i 'X-API-Version')][0] sw (i 'v1/')`,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - ends with simple suffix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "foo$",
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')][0] ew (i 'foo')`, headerName),
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - ends with dotted suffix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "foo\\.bar$",
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')][0] ew (i 'foo.bar')`, headerName),
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				pathPrefix := fake.Lorem().Word()
				pathSuffix := fake.Lorem().Word()
				return testCase{
					name: "regex header match - ends with escaped slash and backslash suffix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: fmt.Sprintf(`.*%s\/\\%s$`, pathPrefix, pathSuffix),
							},
						},
					},
					want: fmt.Sprintf(
						`http.request.headers[(i '%s')][0] ew (i '%s/\%s')`,
						headerName,
						pathPrefix,
						pathSuffix,
					),
				}
			},
			func() testCase {
				return testCase{
					name: "regex header match - rejects mixed prefix and suffix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  "Host",
								Value: `^api.*example\.com$`,
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - unsupported complex regex",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^[a-z]+$",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - rejects single quote in translated prefix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^tenant'" + fake.Lorem().Word() + ".*",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - rejects control character in translated suffix",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: ".*tenant\n" + fake.Lorem().Word() + "$",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - rejects unsafe header name",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName + "'bad"),
								Value: "^tenant" + fake.Lorem().Word() + ".*",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - starts with no anchor",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "foo.*",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - ends with no anchor",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: ".*foo",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				return testCase{
					name: "regex header match - both anchors unsupported",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: "^foo.*bar$",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				return testCase{
					name: "unsupported query param match",
					match: gatewayv1.HTTPRouteMatch{
						QueryParams: []gatewayv1.HTTPQueryParamMatch{
							{
								Type:  lo.ToPtr(gatewayv1.QueryParamMatchExact),
								Name:  "page",
								Value: "1",
							},
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				return testCase{
					name: "unsupported method match",
					match: gatewayv1.HTTPRouteMatch{
						Method: lo.ToPtr(gatewayv1.HTTPMethodPost),
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				return testCase{
					name:  "no matches defined",
					match: gatewayv1.HTTPRouteMatch{},
					want:  "",
				}
			},
			func() testCase {
				return testCase{
					name: "nil path value",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchExact),
							Value: nil, // Invalid config, but test behavior
						},
					},
					wantErrText: "path match value cannot be nil",
				}
			},
			func() testCase {
				fake := faker.New()
				pathValue := "/" + fake.Lorem().Word() + "/" + fake.Lorem().Word()
				return testCase{
					name: "nil path type",
					match: gatewayv1.HTTPRouteMatch{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  nil, // Invalid config
							Value: new(pathValue),
						},
					},
					want: fmt.Sprintf(`http.request.url.path sw '%s'`, pathValue),
				}
			},
			func() testCase {
				fake := faker.New()
				headerName := "X-" + fake.Lorem().Word()
				headerValue := fake.Lorem().Word()
				return testCase{
					name: "nil header type",
					match: gatewayv1.HTTPRouteMatch{
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  nil, // Invalid config
								Name:  gatewayv1.HTTPHeaderName(headerName),
								Value: headerValue,
							},
						},
					},
					want: fmt.Sprintf(`http.request.headers[(i '%s')] eq (i '%s')`, headerName, headerValue),
				}
			},
		}

		for _, tcFunc := range tests {
			tc := tcFunc()
			t.Run(tc.name, func(t *testing.T) {
				rs := newOciLoadBalancerRoutingRulesMapper()
				actual, err := rs.mapHTTPRouteMatchToCondition(tc.match)

				switch {
				case tc.wantErrIs != nil:
					require.ErrorIs(t, err, tc.wantErrIs)
				case tc.wantErrText != "":
					require.ErrorContains(t, err, tc.wantErrText)
				default:
					require.NoError(t, err)
					assert.Equal(
						t,
						strings.Join(strings.Fields(tc.want), " "),
						strings.Join(strings.Fields(actual), " "),
					)
				}
			})
		}
	})

	t.Run("mapHTTPRouteMatchesToCondition", func(t *testing.T) {
		type testCase struct {
			name        string
			matches     []gatewayv1.HTTPRouteMatch
			want        string
			wantErrIs   error
			wantErrText string
		}

		tests := []func() testCase{
			func() testCase {
				fake := faker.New()
				pathValue1 := "/" + fake.Lorem().Word()
				return testCase{
					name: "single match",
					matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchExact),
								Value: new(pathValue1),
							},
						},
					},
					want: fmt.Sprintf(
						`any(http.request.url.path eq '%s')`,
						pathValue1,
					),
				}
			},
			func() testCase {
				fake := faker.New()
				pathValue1 := "/" + fake.Lorem().Word()
				pathValue2 := "/" + fake.Lorem().Word() + "/" + fake.Lorem().Word()
				return testCase{
					name: "multiple matches",
					matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchExact),
								Value: new(pathValue1),
							},
						},
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
								Value: new(pathValue2),
							},
						},
					},
					want: fmt.Sprintf(
						`any(http.request.url.path eq '%s', http.request.url.path sw '%s')`,
						pathValue1, pathValue2,
					),
				}
			},
			func() testCase {
				fake := faker.New()
				pathValue := "/" + fake.Lorem().Word()
				return testCase{
					name: "one unsupported match among others",
					matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchExact),
								Value: new(pathValue),
							},
						},
						{
							Method: lo.ToPtr(gatewayv1.HTTPMethodPost), // Unsupported
						},
					},
					wantErrIs: errUnsupportedMatch,
				}
			},
			func() testCase {
				return testCase{
					name:    "empty matches slice",
					matches: []gatewayv1.HTTPRouteMatch{},
					want:    "",
				}
			},
			func() testCase {
				return testCase{
					name: "only empty match conditions",
					matches: []gatewayv1.HTTPRouteMatch{
						{},
					},
					want: "",
				}
			},
			func() testCase {
				return testCase{
					name: "multiple conditions in a match are wrapped in parentheses in any()",
					matches: []gatewayv1.HTTPRouteMatch{
						{
							Path: &gatewayv1.HTTPPathMatch{
								Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
								Value: new("/"),
							},
							Headers: []gatewayv1.HTTPHeaderMatch{
								{
									Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
									Name:  "host",
									Value: "^argocd-",
								},
							},
						},
					},
					want: "any(all(http.request.url.path sw '/', http.request.headers[(i 'host')][0] sw (i 'argocd-')))",
				}
			},
		}

		for _, tcFunc := range tests {
			tc := tcFunc()
			t.Run(tc.name, func(t *testing.T) {
				rs := newOciLoadBalancerRoutingRulesMapper()
				actual, err := rs.mapHTTPRouteMatchesToCondition(tc.matches)

				switch {
				case tc.wantErrIs != nil:
					require.ErrorIs(t, err, tc.wantErrIs)
				case tc.wantErrText != "":
					require.ErrorContains(t, err, tc.wantErrText)
				default:
					require.NoError(t, err)
					assert.Equal(
						t,
						strings.Join(strings.Fields(tc.want), " "),
						strings.Join(strings.Fields(actual), " "),
					)
				}
			})
		}
	})

	t.Run("mapHTTPRouteHostnamesAndMatchesToCondition", func(t *testing.T) {
		t.Run("uses route matches when no hostnames are configured", func(t *testing.T) {
			fake := faker.New()
			pathValue := "/" + fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapHTTPRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.HTTPRouteMatch{
					{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchExact),
							Value: &pathValue,
						},
					},
				},
			)

			require.NoError(t, err)
			assert.Equal(t, fmt.Sprintf("any(http.request.url.path eq '%s')", pathValue), actual)
		})

		t.Run("combines each hostname with each route match", func(t *testing.T) {
			fake := faker.New()
			host1 := gatewayv1.Hostname("auth-" + fake.Internet().Domain())
			host2 := gatewayv1.Hostname("api-" + fake.Internet().Domain())
			pathValue := "/" + fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapHTTPRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{host1, host2},
				0,
				[]gatewayv1.HTTPRouteMatch{
					{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
							Value: &pathValue,
						},
					},
				},
			)

			require.NoError(t, err)
			want := fmt.Sprintf(
				"any("+
					"all(http.request.headers[(i 'host')] eq (i '%s'), http.request.url.path sw '%s'), "+
					"all(http.request.headers[(i 'host')] eq (i '%s'), http.request.url.path sw '%s')"+
					")",
				host1,
				pathValue,
				host2,
				pathValue,
			)
			assert.Equal(
				t,
				strings.Join(strings.Fields(want), " "),
				strings.Join(strings.Fields(actual), " "),
			)
		})

		t.Run("uses hostnames when matches are empty", func(t *testing.T) {
			fake := faker.New()
			host1 := gatewayv1.Hostname("auth-" + fake.Internet().Domain())
			host2 := gatewayv1.Hostname("api-" + fake.Internet().Domain())

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapHTTPRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{host1, host2},
				0,
				nil,
			)

			require.NoError(t, err)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(http.request.headers[(i 'host')] eq (i '%s'), http.request.headers[(i 'host')] eq (i '%s'))",
					host1,
					host2,
				),
				actual,
			)
		})

		t.Run("matches hostname with listener port", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("api-" + fake.Internet().Domain())
			listenerPort := 8000 + fake.Int32Between(1, 1000)
			pathValue := "/" + fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapHTTPRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				listenerPort,
				[]gatewayv1.HTTPRouteMatch{
					{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
							Value: &pathValue,
						},
					},
				},
			)

			require.NoError(t, err)
			want := fmt.Sprintf(
				"any("+
					"all(http.request.headers[(i 'host')] eq (i '%s'), http.request.url.path sw '%s'), "+
					"all(http.request.headers[(i 'host')] eq (i '%s:%d'), http.request.url.path sw '%s')"+
					")",
				hostname,
				pathValue,
				hostname,
				listenerPort,
				pathValue,
			)
			assert.Equal(t, want, actual)
		})

		t.Run("flattens hostname with path and regex header conditions", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("api-" + fake.Internet().Domain())
			pathValue := "/" + fake.Lorem().Word()
			headerPrefix := "beta-" + fake.Lorem().Word()
			headerName := gatewayv1.HTTPHeaderName("x-" + fake.Lorem().Word())

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapHTTPRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				0,
				[]gatewayv1.HTTPRouteMatch{
					{
						Path: &gatewayv1.HTTPPathMatch{
							Type:  lo.ToPtr(gatewayv1.PathMatchPathPrefix),
							Value: &pathValue,
						},
						Headers: []gatewayv1.HTTPHeaderMatch{
							{
								Type:  lo.ToPtr(gatewayv1.HeaderMatchRegularExpression),
								Name:  headerName,
								Value: "^" + headerPrefix + ".*",
							},
						},
					},
				},
			)

			require.NoError(t, err)
			want := fmt.Sprintf(
				"any(all("+
					"http.request.headers[(i 'host')] eq (i '%s'), "+
					"http.request.url.path sw '%s', "+
					"http.request.headers[(i '%s')][0] sw (i '%s')"+
					"))",
				hostname,
				pathValue,
				headerName,
				headerPrefix,
			)
			assert.Equal(t, want, actual)
			assert.NotContains(t, actual, "all(http.request.url.path")
		})
	})

	t.Run("allRoutingConditions", func(t *testing.T) {
		fake := faker.New()
		hostCondition := fmt.Sprintf(
			"http.request.headers[(i 'host')] eq (i '%s')",
			"api-"+fake.Internet().Domain(),
		)
		pathCondition := fmt.Sprintf("http.request.url.path sw '/%s'", fake.Lorem().Word())
		headerCondition := fmt.Sprintf(
			"http.request.headers[(i 'accept')] eq (i '%s, %s')",
			"text/"+fake.Lorem().Word(),
			"application/"+fake.Lorem().Word(),
		)
		nestedCondition := fmt.Sprintf(
			"any(%s, %s)",
			fmt.Sprintf("http.request.headers[(i 'x-first')] eq (i '%s')", fake.UUID().V4()),
			fmt.Sprintf("http.request.headers[(i 'x-second')] eq (i '%s')", fake.UUID().V4()),
		)

		tests := []struct {
			name       string
			conditions []string
			want       string
		}{
			{
				name: "flattens simple all condition",
				conditions: []string{
					hostCondition,
					fmt.Sprintf("all(%s, %s)", pathCondition, nestedCondition),
				},
				want: fmt.Sprintf("all(%s, %s, %s)", hostCondition, pathCondition, nestedCondition),
			},
			{
				name: "does not split comma inside literal",
				conditions: []string{
					hostCondition,
					fmt.Sprintf("all(%s, %s)", pathCondition, headerCondition),
				},
				want: fmt.Sprintf("all(%s, %s, %s)", hostCondition, pathCondition, headerCondition),
			},
			{
				name: "does not split comma inside nested condition",
				conditions: []string{
					hostCondition,
					fmt.Sprintf("all(%s, %s)", nestedCondition, pathCondition),
				},
				want: fmt.Sprintf("all(%s, %s, %s)", hostCondition, nestedCondition, pathCondition),
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, allRoutingConditions(tc.conditions...))
			})
		}
	})

	t.Run("appendRoutingConditionParts", func(t *testing.T) {
		fake := faker.New()
		pathCondition := fmt.Sprintf("http.request.url.path sw '/%s'", fake.Lorem().Word())
		headerCondition := fmt.Sprintf(
			"http.request.headers[(i 'accept')] eq (i '%s, %s')",
			"text/"+fake.Lorem().Word(),
			"application/"+fake.Lorem().Word(),
		)
		nestedCondition := fmt.Sprintf(
			"any(%s, %s)",
			fmt.Sprintf("http.request.headers[(i 'x-first')] eq (i '%s')", fake.UUID().V4()),
			fmt.Sprintf("http.request.headers[(i 'x-second')] eq (i '%s')", fake.UUID().V4()),
		)

		tests := []struct {
			name      string
			condition string
			want      []string
		}{
			{
				name:      "flattens simple all condition",
				condition: fmt.Sprintf("all(%s, %s)", pathCondition, headerCondition),
				want:      []string{pathCondition, headerCondition},
			},
			{
				name:      "does not split comma inside literal",
				condition: fmt.Sprintf("all(%s, %s)", headerCondition, pathCondition),
				want:      []string{headerCondition, pathCondition},
			},
			{
				name:      "does not split comma inside nested condition",
				condition: fmt.Sprintf("all(%s, %s)", nestedCondition, pathCondition),
				want:      []string{nestedCondition, pathCondition},
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				assert.Equal(t, tc.want, appendRoutingConditionParts(nil, tc.condition))
			})
		}
	})

	t.Run("splitOCIConditionArguments", func(t *testing.T) {
		fake := faker.New()
		firstCondition := fmt.Sprintf("http.request.url.path sw '/%s'", fake.Lorem().Word())
		secondCondition := fmt.Sprintf(
			"http.request.headers[(i 'x-%s')] eq (i '%s')",
			fake.Lorem().Word(),
			fake.UUID().V4(),
		)

		tests := []struct {
			name   string
			value  string
			want   []string
			wantOK bool
		}{
			{
				name:   "splits top level arguments",
				value:  fmt.Sprintf("%s, %s", firstCondition, secondCondition),
				want:   []string{firstCondition, secondCondition},
				wantOK: true,
			},
			{
				name:   "rejects empty input",
				value:  "",
				wantOK: false,
			},
			{
				name:   "rejects comma without following space",
				value:  fmt.Sprintf("%s,%s", firstCondition, secondCondition),
				wantOK: false,
			},
			{
				name:   "rejects empty first argument",
				value:  ", " + secondCondition,
				wantOK: false,
			},
			{
				name:   "rejects empty final argument",
				value:  firstCondition + ", ",
				wantOK: false,
			},
			{
				name:   "rejects unmatched literal",
				value:  fmt.Sprintf("%s, http.request.url.path sw '/%s", firstCondition, fake.Lorem().Word()),
				wantOK: false,
			},
			{
				name:   "rejects unbalanced open paren",
				value:  fmt.Sprintf("any(%s, %s", firstCondition, secondCondition),
				wantOK: false,
			},
			{
				name:   "rejects unbalanced close paren",
				value:  fmt.Sprintf("%s), %s", firstCondition, secondCondition),
				wantOK: false,
			},
		}

		for _, tc := range tests {
			t.Run(tc.name, func(t *testing.T) {
				got, ok := splitOCIConditionArguments(tc.value)

				assert.Equal(t, tc.wantOK, ok)
				assert.Equal(t, tc.want, got)
			})
		}
	})

	t.Run("mapGRPCRouteHostnamesAndMatchesToCondition", func(t *testing.T) {
		grpcBranches := func(prefix []string, suffix ...string) string {
			branches := make([]string, 0, len(grpcContentTypeConditions()))
			for _, contentTypeCondition := range grpcContentTypeConditions() {
				conditions := make([]string, 0, len(prefix)+1+len(suffix))
				conditions = append(conditions, prefix...)
				conditions = append(conditions, contentTypeCondition)
				conditions = append(conditions, suffix...)
				branches = append(branches, allRoutingConditions(conditions...))
			}
			return strings.Join(branches, ", ")
		}

		t.Run("maps service and method to exact grpc path", func(t *testing.T) {
			fake := faker.New()
			service := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())
			method := fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Service: &service,
							Method:  &method,
						},
					},
				},
			)

			require.NoError(t, err)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(%s)",
					grpcBranches(nil, fmt.Sprintf("http.request.url.path eq '/%s/%s'", service, method)),
				),
				actual,
			)
		})

		t.Run("maps service only to grpc path prefix", func(t *testing.T) {
			fake := faker.New()
			service := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Service: &service,
						},
					},
				},
			)

			require.NoError(t, err)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(%s)",
					grpcBranches(nil, fmt.Sprintf("http.request.url.path sw '/%s/'", service)),
				),
				actual,
			)
		})

		t.Run("maps method only to grpc path suffix", func(t *testing.T) {
			fake := faker.New()
			method := fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Method: &method,
						},
					},
				},
			)

			require.NoError(t, err)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(%s)",
					grpcBranches(nil, fmt.Sprintf("http.request.url.path ew '/%s'", method)),
				),
				actual,
			)
		})

		t.Run("combines hostname method and header conditions", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("grpc-" + fake.Internet().Domain())
			service := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())
			method := fake.Lorem().Word()
			headerName := gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word())
			headerValue := fake.UUID().V4()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Service: &service,
							Method:  &method,
						},
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Name:  headerName,
								Value: headerValue,
							},
						},
					},
				},
			)

			require.NoError(t, err)
			hostCondition := fmt.Sprintf("http.request.headers[(i 'host')] eq (i '%s')", hostname)
			matchCondition := fmt.Sprintf(
				"all(http.request.url.path eq '/%s/%s', http.request.headers[(i '%s')] eq (i '%s'))",
				service,
				method,
				headerName,
				headerValue,
			)
			want := fmt.Sprintf(
				"any(%s)",
				grpcBranches([]string{hostCondition}, matchCondition),
			)
			assert.Equal(t, want, actual)
		})

		t.Run("uses hostnames when matches are empty", func(t *testing.T) {
			fake := faker.New()
			host1 := gatewayv1.Hostname("grpc-a-" + fake.Internet().Domain())
			host2 := gatewayv1.Hostname("grpc-b-" + fake.Internet().Domain())

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{host1, host2},
				0,
				nil,
			)

			require.NoError(t, err)
			hostCondition1 := fmt.Sprintf("http.request.headers[(i 'host')] eq (i '%s')", host1)
			hostCondition2 := fmt.Sprintf("http.request.headers[(i 'host')] eq (i '%s')", host2)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(%s, %s)",
					grpcBranches([]string{hostCondition1}),
					grpcBranches([]string{hostCondition2}),
				),
				actual,
			)
		})

		t.Run("matches hostname with listener port", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("grpc-" + fake.Internet().Domain())
			listenerPort := 8000 + fake.Int32Between(1, 1000)

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				listenerPort,
				nil,
			)

			require.NoError(t, err)
			bareHostCondition := fmt.Sprintf("http.request.headers[(i 'host')] eq (i '%s')", hostname)
			portHostCondition := fmt.Sprintf(
				"http.request.headers[(i 'host')] eq (i '%s:%d')",
				hostname,
				listenerPort,
			)
			assert.Equal(
				t,
				fmt.Sprintf(
					"any(%s, %s)",
					grpcBranches([]string{bareHostCondition}),
					grpcBranches([]string{portHostCondition}),
				),
				actual,
			)
		})

		t.Run("returns native grpc content type condition when no grpc matches are configured", func(t *testing.T) {
			rs := newOciLoadBalancerRoutingRulesMapper()

			actual, err := rs.mapGRPCRouteMatchesToCondition(nil)

			require.NoError(t, err)
			assert.Equal(t, grpcContentTypeCondition(), actual)
		})

		t.Run("returns empty condition for an empty grpc match", func(t *testing.T) {
			rs := newOciLoadBalancerRoutingRulesMapper()

			actual, err := rs.mapGRPCRouteMatchToCondition(gatewayv1.GRPCRouteMatch{})

			require.NoError(t, err)
			assert.Empty(t, actual)
		})

		t.Run("returns method validation errors when hostname is configured", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("grpc-" + fake.Internet().Domain())

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				0,
				[]gatewayv1.GRPCRouteMatch{{Method: &gatewayv1.GRPCMethodMatch{}}},
			)

			require.ErrorContains(t, err, "grpc method match requires service or method")
		})

		t.Run("rejects regex method matching", func(t *testing.T) {
			fake := faker.New()
			service := fmt.Sprintf("%s.%s", fake.Lorem().Word(), fake.Lorem().Word())
			matchType := gatewayv1.GRPCMethodMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Type:    &matchType,
							Service: &service,
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("rejects unsafe exact method matching", func(t *testing.T) {
			fake := faker.New()
			service := "svc'" + fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Service: &service,
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("rejects regex header matching", func(t *testing.T) {
			fake := faker.New()
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word()),
								Value: "^[a-z]+$",
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("maps regex host header matching", func(t *testing.T) {
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  "Host",
								Value: `^community-manager-api\..*$`,
							},
						},
					},
				},
			)

			require.NoError(t, err)
			wantHeaderCondition := `http.request.headers[(i 'Host')][0] sw (i 'community-manager-api.')`
			assert.Equal(t, fmt.Sprintf("any(%s)", grpcBranches(nil, wantHeaderCondition)), actual)
		})

		t.Run("maps regex non host header prefix matching", func(t *testing.T) {
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  "X-API-Version",
								Value: `^v1\/`,
							},
						},
					},
				},
			)

			require.NoError(t, err)
			wantHeaderCondition := `http.request.headers[(i 'X-API-Version')][0] sw (i 'v1/')`
			assert.Equal(t, fmt.Sprintf("any(%s)", grpcBranches(nil, wantHeaderCondition)), actual)
		})

		t.Run("maps regex non host header suffix matching", func(t *testing.T) {
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  "X-Tenant",
								Value: `-prod$`,
							},
						},
					},
				},
			)

			require.NoError(t, err)
			wantHeaderCondition := `http.request.headers[(i 'X-Tenant')][0] ew (i '-prod')`
			assert.Equal(t, fmt.Sprintf("any(%s)", grpcBranches(nil, wantHeaderCondition)), actual)
		})

		t.Run("rejects unsupported regex header matching", func(t *testing.T) {
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  "Host",
								Value: `^api-[0-9]+\.example\.com$`,
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("rejects unsafe regex header matching", func(t *testing.T) {
			fake := faker.New()
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word()),
								Value: ".*tenant'" + fake.Lorem().Word() + "$",
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("rejects unsafe exact header matching", func(t *testing.T) {
			fake := faker.New()
			headerType := gatewayv1.GRPCHeaderMatchExact

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word()),
								Value: "tenant'" + fake.Lorem().Word(),
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("rejects unsafe regex header name", func(t *testing.T) {
			fake := faker.New()
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word() + "'bad"),
								Value: "^tenant" + fake.Lorem().Word() + ".*",
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})

		t.Run("flattens hostname with content type method and regex header conditions", func(t *testing.T) {
			fake := faker.New()
			hostname := gatewayv1.Hostname("grpc-" + fake.Internet().Domain())
			service := "svc." + fake.Lorem().Word()
			headerType := gatewayv1.GRPCHeaderMatchRegularExpression
			headerName := gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word())
			headerPrefix := "grpc-" + fake.Lorem().Word()

			rs := newOciLoadBalancerRoutingRulesMapper()
			actual, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				[]gatewayv1.Hostname{hostname},
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Method: &gatewayv1.GRPCMethodMatch{
							Service: &service,
						},
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  headerName,
								Value: "^" + headerPrefix + ".*",
							},
						},
					},
				},
			)

			require.NoError(t, err)
			assert.Contains(
				t,
				actual,
				fmt.Sprintf(
					"all(http.request.headers[(i 'host')] eq (i '%s'), "+
						"http.request.headers[(i 'content-type')][0] eq (i 'application/grpc'), "+
						"http.request.url.path sw '/%s/', "+
						"http.request.headers[(i '%s')][0] sw (i '%s'))",
					hostname,
					service,
					headerName,
					headerPrefix,
				),
			)
			assert.NotContains(t, actual, "all(http.request.url.path")
		})

		t.Run("rejects unknown header match type", func(t *testing.T) {
			fake := faker.New()
			headerType := gatewayv1.GRPCHeaderMatchType("Unknown")

			rs := newOciLoadBalancerRoutingRulesMapper()
			_, err := rs.mapGRPCRouteHostnamesAndMatchesToCondition(
				nil,
				0,
				[]gatewayv1.GRPCRouteMatch{
					{
						Headers: []gatewayv1.GRPCHeaderMatch{
							{
								Type:  &headerType,
								Name:  gatewayv1.GRPCHeaderName("x-" + fake.Lorem().Word()),
								Value: fake.Lorem().Word(),
							},
						},
					},
				},
			)

			require.ErrorIs(t, err, errUnsupportedMatch)
		})
	})
}
