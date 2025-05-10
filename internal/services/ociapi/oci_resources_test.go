package ociapi

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConstructOCIResourceName_Refactored(t *testing.T) {
	type args struct {
		originalName string
		config       OCIResourceNameConfig
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantLen int
	}{
		{
			name: "name fits, no sanitization pattern",
			args: args{
				originalName: "my-perfectly-valid-name",
				config:       OCIResourceNameConfig{MaxLength: 32},
			},
			want:    "my-perfectly-valid-name",
			wantLen: 23,
		},
		{
			name: "name fits, has potentially invalid chars but no pattern provided",
			args: args{
				originalName: "my!name@with#chars",
				config:       OCIResourceNameConfig{MaxLength: 32},
			},
			want:    "my!name@with#chars",
			wantLen: 18,
		},
		{
			name: "name exactly MaxLength, no sanitization pattern",
			args: args{
				originalName: strings.Repeat("a", 32),
				config:       OCIResourceNameConfig{MaxLength: 32},
			},
			want:    strings.Repeat("a", 32),
			wantLen: 32,
		},
		{
			name: "name fits after sanitization",
			args: args{
				originalName: "my!n@me#needs$sanitization",
				config: OCIResourceNameConfig{
					MaxLength:           32,
					InvalidCharsPattern: regexp.MustCompile(`[^a-zA-Z0-9-]`),
				},
			},
			want:    "my_n_me_needs_sanitization",
			wantLen: 26,
		},
		{
			name: "sanitized name is exactly MaxLength",
			args: args{
				originalName: strings.Repeat("a!", 16),
				config: OCIResourceNameConfig{
					MaxLength:           32,
					InvalidCharsPattern: regexp.MustCompile(`!`),
				},
			},
			want:    strings.Repeat("a_", 16),
			wantLen: 32,
		},
		{
			name: "name too long, no sanitization pattern, hash and split",
			args: args{
				originalName: "this-is-a-very-long-name-that-will-be-hashed-and-split",
				config:       OCIResourceNameConfig{MaxLength: 32},
			},
			want: "this-is-a-ve" +
				defaultHashFunc("this-is-a-very-long-name-that-will-be-hashed-and-split") +
				"ed-and-split",
			wantLen: 32,
		},
		{
			name: "name too long, with sanitization, hash original, concat sanitized parts",
			args: args{
				originalName: "this!is!a!very!long!name!that!will!be!hashed!and!split!",
				config: OCIResourceNameConfig{
					MaxLength:           32,
					InvalidCharsPattern: regexp.MustCompile(`!`),
				},
			},
			want: "this_is_a_ve" +
				defaultHashFunc("this!is!a!very!long!name!that!will!be!hashed!and!split!") +
				"d_and_split_",
			wantLen: 32,
		},
		{
			name: "name very long, MaxLength allows only hash and minimal parts",
			args: args{
				originalName: strings.Repeat("abcdefghij", 5),
				config:       OCIResourceNameConfig{MaxLength: 10},
			},
			want:    "a" + defaultHashFunc(strings.Repeat("abcdefghij", 5)) + "j",
			wantLen: 10,
		},
		{
			name: "name long, MaxLength allows hash and odd remaining parts",
			args: args{
				originalName: strings.Repeat("X", 30),
				config:       OCIResourceNameConfig{MaxLength: 11},
			},
			want:    "X" + defaultHashFunc(strings.Repeat("X", 30)) + "XX",
			wantLen: 11,
		},
		{
			name: "MaxLength is less than hash length",
			args: args{
				originalName: "a-long-name-to-force-hash",
				config:       OCIResourceNameConfig{MaxLength: 6},
			},
			want:    defaultHashFunc("a-long-name-to-force-hash")[:6],
			wantLen: 6,
		},
		{
			name: "MaxLength equals hash length",
			args: args{
				originalName: "another-long-name",
				config:       OCIResourceNameConfig{MaxLength: 8},
			},
			want:    defaultHashFunc("another-long-name"),
			wantLen: 8,
		},
		{
			name: "empty originalName, default config (implies MaxLength 0)",
			args: args{
				originalName: "",
				config:       OCIResourceNameConfig{},
			},
			want:    "",
			wantLen: 0,
		},
		{
			name: "empty originalName, with sanitization pattern (no effect), MaxLength 0",
			args: args{
				originalName: "",
				config:       OCIResourceNameConfig{InvalidCharsPattern: regexp.MustCompile(`[^a-z]`)},
			},
			want:    "",
			wantLen: 0,
		},
		{
			name: "originalName becomes empty after sanitization, and fits (MaxLength 5)",
			args: args{
				originalName: "!@#",
				config:       OCIResourceNameConfig{InvalidCharsPattern: regexp.MustCompile("[^a-z]"), MaxLength: 5},
			},
			want:    "___",
			wantLen: 3,
		},
		{
			name: "originalName becomes empty after sanitization, " +
				"but original was non-empty, too long -> hash original (MaxLength 32)",
			args: args{
				originalName: strings.Repeat("@@@", 20),
				config:       OCIResourceNameConfig{InvalidCharsPattern: regexp.MustCompile("@"), MaxLength: 32},
			},
			want: strings.Repeat("_", 12) +
				defaultHashFunc(strings.Repeat("@@@", 20)) +
				strings.Repeat("_", 12),
			wantLen: 32,
		},
		{
			name: "zero-value config, should behave as no limit and no sanitization",
			args: args{
				originalName: "my-name-with-nil-config-long-enough-to-be-hashed-if-limited",
				config:       OCIResourceNameConfig{},
			},
			want:    "my-name-with-nil-config-long-enough-to-be-hashed-if-limited",
			wantLen: 59,
		},
		{
			name: "MaxLength is 0 (no limit), name is short",
			args: args{
				originalName: "short",
				config:       OCIResourceNameConfig{MaxLength: 0},
			},
			want:    "short",
			wantLen: 5,
		},
		{
			name: "MaxLength is 0 (no limit), name is long, no sanitization",
			args: args{
				originalName: "this-is-a-very-long-name-that-would-normally-be-hashed-and-split-but-should-not-be",
				config:       OCIResourceNameConfig{MaxLength: 0},
			},
			want:    "this-is-a-very-long-name-that-would-normally-be-hashed-and-split-but-should-not-be",
			wantLen: 82,
		},
		{
			name: "MaxLength is 0 (no limit), name is long, with sanitization",
			args: args{
				originalName: "this!is!a!very!long!name!that!would!be!sanitized!but!not!hashed!or!split!",
				config:       OCIResourceNameConfig{MaxLength: 0, InvalidCharsPattern: regexp.MustCompile(`!`)},
			},
			want:    "this_is_a_very_long_name_that_would_be_sanitized_but_not_hashed_or_split_",
			wantLen: 73,
		},
		{
			name: "MaxLength is negative (no limit), name is long",
			args: args{
				originalName: "another-very-long-name-for-negative-maxlength-test-case",
				config:       OCIResourceNameConfig{MaxLength: -5},
			},
			want:    "another-very-long-name-for-negative-maxlength-test-case",
			wantLen: 55,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ConstructOCIResourceName(tt.args.originalName, tt.args.config)

			require.Equal(t, tt.want, got,
				"Test case: %s - ConstructOCIResourceName() got = %v, want %v", tt.name, got, tt.want)

			// Check length if wantLen is specified and positive
			if tt.wantLen > 0 {
				require.Len(t, got, tt.wantLen,
					"Test case: %s - ConstructOCIResourceName() output length. got = %d, want %d", tt.name, len(got), tt.wantLen)
			}

			// If MaxLength is positive, ensure output length does not exceed it.
			// If MaxLength <= 0, it means no limit, so this check is skipped.
			if tt.args.config.MaxLength > 0 {
				require.LessOrEqual(t, len(got), tt.args.config.MaxLength,
					"Test case: %s - Length of got (%d) should be <= MaxLength (%d)", tt.name, len(got), tt.args.config.MaxLength)
			}

			// Verify no invalid characters in the output if a pattern was provided
			if tt.args.config.InvalidCharsPattern != nil && got != "" {
				for _, r := range got {
					char := string(r)
					if char != "_" && tt.args.config.InvalidCharsPattern.MatchString(char) {
						t.Errorf("Test case: %s - Output '%s' contains invalid char '%s' (that is not '_') according to pattern '%s'",
							tt.name, got, char, tt.args.config.InvalidCharsPattern.String())
					}
				}
			}
		})
	}
}
