// Code generated by mockery. DO NOT EDIT.

//go:build !release

package app

import (
	context "context"

	client "sigs.k8s.io/controller-runtime/pkg/client"

	mock "github.com/stretchr/testify/mock"

	types "k8s.io/apimachinery/pkg/types"
)

// Mockk8sClient is an autogenerated mock type for the k8sClient type
type Mockk8sClient struct {
	mock.Mock
}

type Mockk8sClient_Expecter struct {
	mock *mock.Mock
}

func (_m *Mockk8sClient) EXPECT() *Mockk8sClient_Expecter {
	return &Mockk8sClient_Expecter{mock: &_m.Mock}
}

// Get provides a mock function with given fields: ctx, key, obj, opts
func (_m *Mockk8sClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	_va := make([]interface{}, len(opts))
	for _i := range opts {
		_va[_i] = opts[_i]
	}
	var _ca []interface{}
	_ca = append(_ca, ctx, key, obj)
	_ca = append(_ca, _va...)
	ret := _m.Called(_ca...)

	if len(ret) == 0 {
		panic("no return value specified for Get")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, types.NamespacedName, client.Object, ...client.GetOption) error); ok {
		r0 = rf(ctx, key, obj, opts...)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Mockk8sClient_Get_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Get'
type Mockk8sClient_Get_Call struct {
	*mock.Call
}

// Get is a helper method to define mock.On call
//   - ctx context.Context
//   - key types.NamespacedName
//   - obj client.Object
//   - opts ...client.GetOption
func (_e *Mockk8sClient_Expecter) Get(ctx interface{}, key interface{}, obj interface{}, opts ...interface{}) *Mockk8sClient_Get_Call {
	return &Mockk8sClient_Get_Call{Call: _e.mock.On("Get",
		append([]interface{}{ctx, key, obj}, opts...)...)}
}

func (_c *Mockk8sClient_Get_Call) Run(run func(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption)) *Mockk8sClient_Get_Call {
	_c.Call.Run(func(args mock.Arguments) {
		variadicArgs := make([]client.GetOption, len(args)-3)
		for i, a := range args[3:] {
			if a != nil {
				variadicArgs[i] = a.(client.GetOption)
			}
		}
		run(args[0].(context.Context), args[1].(types.NamespacedName), args[2].(client.Object), variadicArgs...)
	})
	return _c
}

func (_c *Mockk8sClient_Get_Call) Return(_a0 error) *Mockk8sClient_Get_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *Mockk8sClient_Get_Call) RunAndReturn(run func(context.Context, types.NamespacedName, client.Object, ...client.GetOption) error) *Mockk8sClient_Get_Call {
	_c.Call.Return(run)
	return _c
}

// List provides a mock function with given fields: ctx, list, opts
func (_m *Mockk8sClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	_va := make([]interface{}, len(opts))
	for _i := range opts {
		_va[_i] = opts[_i]
	}
	var _ca []interface{}
	_ca = append(_ca, ctx, list)
	_ca = append(_ca, _va...)
	ret := _m.Called(_ca...)

	if len(ret) == 0 {
		panic("no return value specified for List")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, client.ObjectList, ...client.ListOption) error); ok {
		r0 = rf(ctx, list, opts...)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Mockk8sClient_List_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'List'
type Mockk8sClient_List_Call struct {
	*mock.Call
}

// List is a helper method to define mock.On call
//   - ctx context.Context
//   - list client.ObjectList
//   - opts ...client.ListOption
func (_e *Mockk8sClient_Expecter) List(ctx interface{}, list interface{}, opts ...interface{}) *Mockk8sClient_List_Call {
	return &Mockk8sClient_List_Call{Call: _e.mock.On("List",
		append([]interface{}{ctx, list}, opts...)...)}
}

func (_c *Mockk8sClient_List_Call) Run(run func(ctx context.Context, list client.ObjectList, opts ...client.ListOption)) *Mockk8sClient_List_Call {
	_c.Call.Run(func(args mock.Arguments) {
		variadicArgs := make([]client.ListOption, len(args)-2)
		for i, a := range args[2:] {
			if a != nil {
				variadicArgs[i] = a.(client.ListOption)
			}
		}
		run(args[0].(context.Context), args[1].(client.ObjectList), variadicArgs...)
	})
	return _c
}

func (_c *Mockk8sClient_List_Call) Return(_a0 error) *Mockk8sClient_List_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *Mockk8sClient_List_Call) RunAndReturn(run func(context.Context, client.ObjectList, ...client.ListOption) error) *Mockk8sClient_List_Call {
	_c.Call.Return(run)
	return _c
}

// Status provides a mock function with no fields
func (_m *Mockk8sClient) Status() client.SubResourceWriter {
	ret := _m.Called()

	if len(ret) == 0 {
		panic("no return value specified for Status")
	}

	var r0 client.SubResourceWriter
	if rf, ok := ret.Get(0).(func() client.SubResourceWriter); ok {
		r0 = rf()
	} else {
		if ret.Get(0) != nil {
			r0 = ret.Get(0).(client.SubResourceWriter)
		}
	}

	return r0
}

// Mockk8sClient_Status_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Status'
type Mockk8sClient_Status_Call struct {
	*mock.Call
}

// Status is a helper method to define mock.On call
func (_e *Mockk8sClient_Expecter) Status() *Mockk8sClient_Status_Call {
	return &Mockk8sClient_Status_Call{Call: _e.mock.On("Status")}
}

func (_c *Mockk8sClient_Status_Call) Run(run func()) *Mockk8sClient_Status_Call {
	_c.Call.Run(func(args mock.Arguments) {
		run()
	})
	return _c
}

func (_c *Mockk8sClient_Status_Call) Return(_a0 client.SubResourceWriter) *Mockk8sClient_Status_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *Mockk8sClient_Status_Call) RunAndReturn(run func() client.SubResourceWriter) *Mockk8sClient_Status_Call {
	_c.Call.Return(run)
	return _c
}

// Update provides a mock function with given fields: ctx, obj, opts
func (_m *Mockk8sClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	_va := make([]interface{}, len(opts))
	for _i := range opts {
		_va[_i] = opts[_i]
	}
	var _ca []interface{}
	_ca = append(_ca, ctx, obj)
	_ca = append(_ca, _va...)
	ret := _m.Called(_ca...)

	if len(ret) == 0 {
		panic("no return value specified for Update")
	}

	var r0 error
	if rf, ok := ret.Get(0).(func(context.Context, client.Object, ...client.UpdateOption) error); ok {
		r0 = rf(ctx, obj, opts...)
	} else {
		r0 = ret.Error(0)
	}

	return r0
}

// Mockk8sClient_Update_Call is a *mock.Call that shadows Run/Return methods with type explicit version for method 'Update'
type Mockk8sClient_Update_Call struct {
	*mock.Call
}

// Update is a helper method to define mock.On call
//   - ctx context.Context
//   - obj client.Object
//   - opts ...client.UpdateOption
func (_e *Mockk8sClient_Expecter) Update(ctx interface{}, obj interface{}, opts ...interface{}) *Mockk8sClient_Update_Call {
	return &Mockk8sClient_Update_Call{Call: _e.mock.On("Update",
		append([]interface{}{ctx, obj}, opts...)...)}
}

func (_c *Mockk8sClient_Update_Call) Run(run func(ctx context.Context, obj client.Object, opts ...client.UpdateOption)) *Mockk8sClient_Update_Call {
	_c.Call.Run(func(args mock.Arguments) {
		variadicArgs := make([]client.UpdateOption, len(args)-2)
		for i, a := range args[2:] {
			if a != nil {
				variadicArgs[i] = a.(client.UpdateOption)
			}
		}
		run(args[0].(context.Context), args[1].(client.Object), variadicArgs...)
	})
	return _c
}

func (_c *Mockk8sClient_Update_Call) Return(_a0 error) *Mockk8sClient_Update_Call {
	_c.Call.Return(_a0)
	return _c
}

func (_c *Mockk8sClient_Update_Call) RunAndReturn(run func(context.Context, client.Object, ...client.UpdateOption) error) *Mockk8sClient_Update_Call {
	_c.Call.Return(run)
	return _c
}

// NewMockk8sClient creates a new instance of Mockk8sClient. It also registers a testing interface on the mock and a cleanup function to assert the mocks expectations.
// The first argument is typically a *testing.T value.
func NewMockk8sClient(t interface {
	mock.TestingT
	Cleanup(func())
}) *Mockk8sClient {
	mock := &Mockk8sClient{}
	mock.Mock.Test(t)

	t.Cleanup(func() { mock.AssertExpectations(t) })

	return mock
}
