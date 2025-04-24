package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrors(t *testing.T) {
	t.Run("resourceStatusError", func(t *testing.T) {
		t.Run("Error() without cause", func(t *testing.T) {
			err := resourceStatusError{
				conditionType: faker.Word(),
				reason:        faker.Sentence(),
				message:       faker.Paragraph(),
			}
			expected := fmt.Sprintf(
				"resourceStatusError: type=%s, reason=%s, message=%s",
				err.conditionType,
				err.reason,
				err.message,
			)
			assert.Equal(t, expected, err.Error())
		})

		t.Run("Error() with cause", func(t *testing.T) {
			causeMsg := faker.Sentence()
			cause := errors.New(causeMsg)
			err := resourceStatusError{
				conditionType: faker.Word(),
				reason:        faker.Sentence(),
				message:       faker.Paragraph(),
				cause:         cause,
			}
			expected := fmt.Sprintf(
				"resourceStatusError: type=%s, reason=%s, message=%s, cause=%s",
				err.conditionType,
				err.reason,
				err.message,
				err.cause,
			)
			assert.Equal(t, expected, err.Error())
		})
	})

	t.Run("ReconcileError", func(t *testing.T) {
		t.Run("NewReconcileError", func(t *testing.T) {
			message := faker.Sentence()
			randInt, fakerErr := faker.RandomInt(0, 1)
			require.NoError(t, fakerErr)
			retriable := randInt[0] == 1 // Randomly true or false

			err := NewReconcileError(message, retriable)

			assert.NotNil(t, err)
			assert.Equal(t, retriable, err.retriable)
			assert.Equal(t, message, err.message)
			require.NoError(t, err.cause)

			expectedMsg := fmt.Sprintf("reconcileError: retriable=%t, message=%s", retriable, message)
			assert.Equal(t, expectedMsg, err.Error())
		})

		t.Run("NewReconcileErrorWithCause", func(t *testing.T) {
			message := faker.Sentence()
			randInt, fakerErr := faker.RandomInt(0, 1)
			require.NoError(t, fakerErr)
			retriable := randInt[0] == 1 // Randomly true or false
			causeMsg := faker.Sentence()
			cause := errors.New(causeMsg)

			err := NewReconcileErrorWithCause(message, retriable, cause)

			assert.NotNil(t, err)
			assert.Equal(t, retriable, err.retriable)
			assert.Equal(t, message, err.message)
			assert.Equal(t, cause, err.cause)

			expectedMsg := fmt.Sprintf("reconcileError: retriable=%t, message=%s, cause=%s", retriable, message, cause)
			assert.Equal(t, expectedMsg, err.Error())
		})

		t.Run("IsRetriable", func(t *testing.T) {
			t.Run("when true", func(t *testing.T) {
				err := NewReconcileError(faker.Sentence(), true)
				assert.True(t, err.IsRetriable())
			})
			t.Run("when false", func(t *testing.T) {
				err := NewReconcileError(faker.Sentence(), false)
				assert.False(t, err.IsRetriable())
			})
			t.Run("with cause when true", func(t *testing.T) {
				err := NewReconcileErrorWithCause(faker.Sentence(), true, errors.New(faker.Sentence()))
				assert.True(t, err.IsRetriable())
			})
			t.Run("with cause when false", func(t *testing.T) {
				err := NewReconcileErrorWithCause(faker.Sentence(), false, errors.New(faker.Sentence()))
				assert.False(t, err.IsRetriable())
			})
		})
	})
}
