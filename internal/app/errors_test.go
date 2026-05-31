package app

import (
	"errors"
	"fmt"
	"testing"

	"github.com/jaswdr/faker/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestErrors(t *testing.T) {
	t.Run("resourceStatusError", func(t *testing.T) {
		t.Run("Error() without cause", func(t *testing.T) {
			fake := faker.New()
			err := resourceStatusError{
				conditionType: fake.Lorem().Word(),
				reason:        fake.Lorem().Sentence(10),
				message:       fake.Lorem().Paragraph(3),
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
			fake := faker.New()
			causeMsg := fake.Lorem().Sentence(10)
			cause := errors.New(causeMsg)
			err := resourceStatusError{
				conditionType: fake.Lorem().Word(),
				reason:        fake.Lorem().Sentence(10),
				message:       fake.Lorem().Paragraph(3),
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
			fake := faker.New()
			message := fake.Lorem().Sentence(10)
			retriable := fake.IntBetween(0, 1) == 1

			err := NewReconcileError(message, retriable)

			assert.NotNil(t, err)
			assert.Equal(t, retriable, err.retriable)
			assert.Equal(t, message, err.message)
			require.NoError(t, err.cause)

			expectedMsg := fmt.Sprintf("reconcileError: retriable=%t, message=%s", retriable, message)
			assert.Equal(t, expectedMsg, err.Error())
		})

		t.Run("NewReconcileErrorWithCause", func(t *testing.T) {
			fake := faker.New()
			message := fake.Lorem().Sentence(10)
			retriable := fake.IntBetween(0, 1) == 1
			causeMsg := fake.Lorem().Sentence(10)
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
				fake := faker.New()
				err := NewReconcileError(fake.Lorem().Sentence(10), true)
				assert.True(t, err.IsRetriable())
			})
			t.Run("when false", func(t *testing.T) {
				fake := faker.New()
				err := NewReconcileError(fake.Lorem().Sentence(10), false)
				assert.False(t, err.IsRetriable())
			})
			t.Run("with cause when true", func(t *testing.T) {
				fake := faker.New()
				message := fake.Lorem().Sentence(10)
				cause := errors.New(fake.Lorem().Sentence(10))
				err := NewReconcileErrorWithCause(message, true, cause)
				assert.True(t, err.IsRetriable())
			})
			t.Run("with cause when false", func(t *testing.T) {
				fake := faker.New()
				message := fake.Lorem().Sentence(10)
				cause := errors.New(fake.Lorem().Sentence(10))
				err := NewReconcileErrorWithCause(message, false, cause)
				assert.False(t, err.IsRetriable())
			})
		})
	})
}
