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
}
