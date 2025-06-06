// Code generated by mockery. DO NOT EDIT.

//go:build !release

package app

import (
	mock "github.com/stretchr/testify/mock"
	v1 "sigs.k8s.io/gateway-api/apis/v1"
)

// MockociLoadBalancerRoutingRulesMapper is an autogenerated mock type for the ociLoadBalancerRoutingRulesMapper type
type MockociLoadBalancerRoutingRulesMapper struct {
	mock.Mock
}

type MockociLoadBalancerRoutingRulesMapper_Expecter struct {
	mock *mock.Mock
}

func (_m *MockociLoadBalancerRoutingRulesMapper) EXPECT() *MockociLoadBalancerRoutingRulesMapper_Expecter {
	return &MockociLoadBalancerRoutingRulesMapper_Expecter{mock: &_m.Mock}
}

// mapHTTPRouteMatchToCondition provides a mock function with given fields: match
func (_m *MockociLoadBalancerRoutingRulesMapper) mapHTTPRouteMatchToCondition(match v1.HTTPRouteMatch) (string, error) {
	ret := _m.Called(match)

	if len(ret) == 0 {
		panic("no return value specified for mapHTTPRouteMatchToCondition")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func(v1.HTTPRouteMatch) (string, error)); ok {
		return rf(match)
	}
	if rf, ok := ret.Get(0).(func(v1.HTTPRouteMatch) string); ok {
		r0 = rf(match)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func(v1.HTTPRouteMatch) error); ok {
		r1 = rf(match)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'mapHTTPRouteMatchToCondition'
type MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call struct {
	*mock.Call
}

// mapHTTPRouteMatchToCondition is a helper method to define mock.On call
//   - match v1.HTTPRouteMatch
func (_e *MockociLoadBalancerRoutingRulesMapper_Expecter) mapHTTPRouteMatchToCondition(match interface{}) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call {
	return &MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call{Call: _e.mock.On("mapHTTPRouteMatchToCondition", match)}
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call) Run(run func(match v1.HTTPRouteMatch)) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].(v1.HTTPRouteMatch))
	})
	return _c
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call) Return(_a0 string, _a1 error) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call) RunAndReturn(run func(v1.HTTPRouteMatch) (string, error)) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchToCondition_Call {
	_c.Call.Return(run)
	return _c
}

// mapHTTPRouteMatchesToCondition provides a mock function with given fields: matches
func (_m *MockociLoadBalancerRoutingRulesMapper) mapHTTPRouteMatchesToCondition(matches []v1.HTTPRouteMatch) (string, error) {
	ret := _m.Called(matches)

	if len(ret) == 0 {
		panic("no return value specified for mapHTTPRouteMatchesToCondition")
	}

	var r0 string
	var r1 error
	if rf, ok := ret.Get(0).(func([]v1.HTTPRouteMatch) (string, error)); ok {
		return rf(matches)
	}
	if rf, ok := ret.Get(0).(func([]v1.HTTPRouteMatch) string); ok {
		r0 = rf(matches)
	} else {
		r0 = ret.Get(0).(string)
	}

	if rf, ok := ret.Get(1).(func([]v1.HTTPRouteMatch) error); ok {
		r1 = rf(matches)
	} else {
		r1 = ret.Error(1)
	}

	return r0, r1
}

// MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'mapHTTPRouteMatchesToCondition'
type MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call struct {
	*mock.Call
}

// mapHTTPRouteMatchesToCondition is a helper method to define mock.On call
//   - matches []v1.HTTPRouteMatch
func (_e *MockociLoadBalancerRoutingRulesMapper_Expecter) mapHTTPRouteMatchesToCondition(matches interface{}) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call {
	return &MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call{Call: _e.mock.On("mapHTTPRouteMatchesToCondition", matches)}
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call) Run(run func(matches []v1.HTTPRouteMatch)) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run(args[0].([]v1.HTTPRouteMatch))
	})
	return _c
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call) Return(_a0 string, _a1 error) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call {
	_c.Call.Return(_a0, _a1)
	return _c
}

func (_c *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call) RunAndReturn(run func([]v1.HTTPRouteMatch) (string, error)) *MockociLoadBalancerRoutingRulesMapper_mapHTTPRouteMatchesToCondition_Call {
	_c.Call.Return(run)
	return _c
}

// NewMockociLoadBalancerRoutingRulesMapper creates a new instance of MockociLoadBalancerRoutingRulesMapper. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockociLoadBalancerRoutingRulesMapper(t interface {
	mock.TestingT
	Cleanup(func())
}) *MockociLoadBalancerRoutingRulesMapper {
	mock := &MockociLoadBalancerRoutingRulesMapper{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
