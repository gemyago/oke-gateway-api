package ociapi

import (
	"fmt"
	"hash/crc32"
	"math/rand/v2"
	"regexp"
	"testing"

	"github.com/gemyago/oke-gateway-api/internal/services"
	"github.com/go-faker/faker/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestConstructOCIResourceName(t *testing.T) {
	t.Run("behavior", func(t *testing.T) {
		t.Run("no limits", func(t *testing.T) {
			inputLength := 15 + rand.IntN(30)
			input := services.RandomString(inputLength)
			got := ConstructOCIResourceName(input, OCIResourceNameConfig{})
			require.Equal(t, input, got)
		})

		t.Run("no limit with sanitize", func(t *testing.T) {
			inputLength := 15 + rand.IntN(30)
			input := services.RandomString(inputLength)
			wantSanitizedName := faker.UUIDDigit()
			sanitizeCalled := false
			wantPattern := regexp.MustCompile(`[^a-zA-Z0-9]+`)
			got := ConstructOCIResourceName(input, OCIResourceNameConfig{
				InvalidCharsPattern: wantPattern,
				sanitizeFunc: func(name string, pattern *regexp.Regexp) string {
					sanitizeCalled = true
					assert.Equal(t, input, name)
					assert.Equal(t, wantPattern, pattern)
					return wantSanitizedName
				},
			})
			require.Equal(t, wantSanitizedName, got)
			require.True(t, sanitizeCalled)
		})

		t.Run("with limit no sanitize (generic)", func(t *testing.T) {
			input := "|is-" + services.RandomString(20+rand.IntN(30)) + "-ie|"
			inputLength := len(input)
			wantHash := "|hs-" + services.RandomString(5+rand.IntN(10)) + "-he|"
			wantMaxLength := inputLength - 2

			hashStarts := inputLength/2 - len(wantHash)/2
			hashEnds := hashStarts + len(wantHash)
			remainingLength := wantMaxLength - hashEnds
			wantResult := input[:hashStarts] + wantHash + input[inputLength-remainingLength:]

			got := ConstructOCIResourceName(input, OCIResourceNameConfig{
				MaxLength: wantMaxLength,
				hashFunc: func(hashInput string) string {
					assert.Equal(t, input, hashInput)
					return wantHash
				},
			})
			require.Equal(t, wantResult, got)
		})

		t.Run("with limit with sanitize (generic)", func(t *testing.T) {
			input := "|is-" + services.RandomString(20+rand.IntN(30)) + "-ie|"
			inputLength := len(input)
			wantHash := "|hs-" + services.RandomString(5+rand.IntN(10)) + "-he|"
			wantMaxLength := inputLength - 2

			hashStarts := inputLength/2 - len(wantHash)/2
			hashEnds := hashStarts + len(wantHash)
			remainingLength := wantMaxLength - hashEnds
			wantResult := input[:hashStarts] + wantHash + input[inputLength-remainingLength:]

			sanitized := false
			wantPattern := regexp.MustCompile(`[^a-zA-Z0-9]+`)
			got := ConstructOCIResourceName(input, OCIResourceNameConfig{
				MaxLength:           wantMaxLength,
				InvalidCharsPattern: wantPattern,
				sanitizeFunc: func(name string, pattern *regexp.Regexp) string {
					assert.Equal(t, input, name)
					assert.Equal(t, wantPattern, pattern)
					sanitized = true
					return input
				},
				hashFunc: func(hashInput string) string {
					assert.Equal(t, input, hashInput)
					return wantHash
				},
			})
			require.Equal(t, wantResult, got)
			require.True(t, sanitized)
		})

		t.Run("with limit no sanitize fixed cases", func(t *testing.T) {
			type testCase struct {
				input string
				hash  string
				limit int
				want  string
			}
			// |is-012345678901234567890123456789-ie| - 38 chars
			// |hs-0123456789-he| - 18 chars
			testCases := []testCase{
				// case0 - limit same as input - input is returned
				{
					input: "|is-012345678901234567890123456789-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 38,
					want:  "|is-012345678901234567890123456789-ie|",
				},
				// case1 - even input, even hash - odd limit
				{
					input: "|is-012345678901234567890123456789-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 37,

					// 38 / 2 = 19
					// 18 / 2 = 9
					// 19 - 9 = 10 - hash should start at 10
					// 19 + 9 = 28 - hash should end at 28
					// 37 - 28 = 9 - chars of input remaining
					// 38 - 9 = 29 - starting part of remaining input
					want: "|is-012345|hs-0123456789-he|56789-ie|",
				},
				// case2 - even input, even hash - even limit
				{
					input: "|is-012345678901234567890123456789-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 36,

					// 38 / 2 = 19
					// 18 / 2 = 9
					// 19 - 9 = 10 - hash should start at 10
					// 19 + 9 = 28 - hash should end at 28
					// 36 - 28 = 8 - chars of input remaining
					// 38 - 8 = 30 - starting part of remaining input
					want: "|is-012345|hs-0123456789-he|6789-ie|",
				},
				// case3 - odd input, even hash - odd limit
				{
					input: "|is-01234567890123456789012345678-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 36,

					// 37 / 2 = 18
					// 18 / 2 = 9
					// 18 - 9 = 9 - hash should start at 9
					// 18 + 9 = 27 - hash should end at 27
					// 36 - 27 = 9 - chars of input remaining
					// 37 - 9 = 28 - starting part of remaining input
					want: "|is-01234|hs-0123456789-he|45678-ie|",
				},
				// case4 - limit same as hash - hash is returned
				{
					input: "|is-012345678901234567890123456789-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 18,
					want:  "|hs-0123456789-he|",
				},
				// case5 - limit less than hash - truncated hash is returned
				{
					input: "|is-012345678901234567890123456789-ie|",
					hash:  "|hs-0123456789-he|",
					limit: 17,
					want:  "|hs-0123456789-he",
				},
			}
			for i, tc := range testCases {
				t.Run(fmt.Sprintf("case %d", i), func(t *testing.T) {
					got := ConstructOCIResourceName(tc.input, OCIResourceNameConfig{
						MaxLength: tc.limit,
						hashFunc: func(_ string) string {
							return tc.hash
						},
					})
					require.Equal(t, tc.want, got)
					assert.GreaterOrEqual(t, len(tc.input), tc.limit, "input length should be greater or equal to limit")
					assert.Len(t, got, tc.limit, "got length should be equal to limit")
				})
			}
		})
	})

	t.Run("defaultHashFunc", func(t *testing.T) {
		input := services.RandomString(10 + rand.IntN(30))
		want := fmt.Sprintf("%08x", crc32.ChecksumIEEE([]byte(input)))

		got := defaultHashFunc(input)
		require.Equal(t, want, got)
	})

	t.Run("defaultSanitizeFunc", func(t *testing.T) {
		t.Run("no pattern", func(t *testing.T) {
			input := services.RandomString(10 + rand.IntN(30))
			got := defaultSanitizeFunc(input, nil)
			require.Equal(t, input, got)
		})

		t.Run("pattern", func(t *testing.T) {
			input := faker.UUIDHyphenated()
			wantPattern := regexp.MustCompile(`[^a-zA-Z0-9]+`)
			got := defaultSanitizeFunc(input, wantPattern)
			require.Equal(t, wantPattern.ReplaceAllString(input, "_"), got)
		})
	})
}
