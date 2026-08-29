package ociapi

import (
	"net/http"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
)

func TestMockServiceError(t *testing.T) {
	fake := faker.New()
	statusCode := http.StatusTooManyRequests
	code := "code-" + fake.Lorem().Word()
	message := "message-" + fake.Lorem().Sentence(5)

	err := NewRandomServiceError(
		RandomServiceErrorWithStatusCode(statusCode),
		RandomServiceErrorWithCode(code),
		RandomServiceErrorWithMessage(message),
	)

	assert.Equal(t, statusCode, err.GetHTTPStatusCode())
	assert.Equal(t, code, err.GetCode())
	assert.Equal(t, message, err.GetMessage())
	assert.NotEmpty(t, err.GetOpcRequestID())
	assert.Contains(t, err.Error(), message)
}
