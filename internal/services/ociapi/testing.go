//go:build !release

package ociapi

import (
	"fmt"

	"github.com/jaswdr/faker/v2"
	"github.com/oracle/oci-go-sdk/v65/common"
)

const (
	randomServiceErrorSentenceWords = 10
	minServiceErrorStatusCode       = 100
	maxServiceErrorStatusCode       = 498
)

type MockServiceError struct {
	statusCode   int
	message      string
	code         string
	opcRequestID string
}

type RandomServiceErrorOpts func(*MockServiceError)

func NewRandomServiceError(opts ...RandomServiceErrorOpts) *MockServiceError {
	fake := faker.New()
	res := &MockServiceError{
		statusCode:   fake.IntBetween(minServiceErrorStatusCode, maxServiceErrorStatusCode),
		message:      fake.Lorem().Sentence(randomServiceErrorSentenceWords),
		code:         fake.Lorem().Word(),
		opcRequestID: fake.UUID().V4(),
	}

	for _, opt := range opts {
		opt(res)
	}

	return res
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

func RandomServiceErrorWithStatusCode(statusCode int) RandomServiceErrorOpts {
	return func(m *MockServiceError) {
		m.statusCode = statusCode
	}
}

func RandomServiceErrorWithCode(code string) RandomServiceErrorOpts {
	return func(m *MockServiceError) {
		m.code = code
	}
}

func RandomServiceErrorWithMessage(message string) RandomServiceErrorOpts {
	return func(m *MockServiceError) {
		m.message = message
	}
}
