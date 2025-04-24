//go:build !release

package ociapi

import (
	"fmt"
	"math/rand/v2"

	"github.com/go-faker/faker/v4"
	"github.com/oracle/oci-go-sdk/v65/common"
)

type MockServiceError struct {
	statusCode   int
	message      string
	code         string
	opcRequestID string
}

func (m *MockServiceError) GetHTTPStatusCode() int {
	return m.statusCode
}

func (m *MockServiceError) GetMessage() string {
	return m.message
}

func (m *MockServiceError) GetCode() string {
	return m.code
}

func (m *MockServiceError) GetOpcRequestID() string {
	return m.opcRequestID
}

func (m *MockServiceError) Error() string {
	return fmt.Sprintf("mock service error statusCode: %d, message: %s", m.statusCode, m.message)
}

var _ common.ServiceError = &MockServiceError{}
var _ error = &MockServiceError{}

type RandomServiceErrorOpts func(*MockServiceError)

func NewRandomServiceError(opts ...RandomServiceErrorOpts) *MockServiceError {
	res := &MockServiceError{
		statusCode:   rand.IntN(399) + 100,
		message:      faker.Sentence(),
		code:         faker.Word(),
		opcRequestID: faker.UUIDHyphenated(),
	}

	for _, opt := range opts {
		opt(res)
	}

	return res
}

func RandomServiceErrorWithStatusCode(statusCode int) RandomServiceErrorOpts {
	return func(m *MockServiceError) {
		m.statusCode = statusCode
	}
}
