package ociapi

import (
	"fmt"
	"hash/crc32"
	"regexp"
	"strings"
)

const (
	ociResourceNameHashPartDivider = 2
)

// OCIResourceNameConfig holds configuration for generating OCI resource names.
type OCIResourceNameConfig struct {
	MaxLength           int
	InvalidCharsPattern *regexp.Regexp // Optional: for sanitization
}

// defaultHashFunc calculates a CRC32 checksum and returns it as an 8-character lowercase hexadecimal string.
func defaultHashFunc(input string) string {
	checksum := crc32.ChecksumIEEE([]byte(input))
	return fmt.Sprintf("%08x", checksum)
}

// defaultSanitizeFunc replaces characters matching the provided pattern with an underscore.
// If no pattern is provided, or name is empty, it returns the name as is.
func defaultSanitizeFunc(name string, pattern *regexp.Regexp) string {
	if name == "" || pattern == nil {
		return name
	}
	return pattern.ReplaceAllString(name, "_")
}

// calculateFinalEndPart determines the ending part of the sanitized name when constructing a hashed resource name.
// sLen: length of the (potentially) sanitized name.
// startPartLen: calculated length of the starting part of the sanitized name.
// endPartLen: calculated desired length of the ending part of the sanitized name.
// sanitizedName: the (potentially) sanitized name string.
func calculateFinalEndPart(sLen, startPartLen, endPartLen int, sanitizedName string) string {
	if endPartLen <= 0 {
		return ""
	}
	if startPartLen >= sLen {
		return ""
	}

	startIndexOfEnd := sLen - endPartLen
	switch {
	case startIndexOfEnd < startPartLen:
		if startPartLen < sLen {
			return sanitizedName[startPartLen:]
		}
		return ""
	case startIndexOfEnd < sLen:
		return sanitizedName[startIndexOfEnd:]
	default:
		return ""
	}
}

// ConstructOCIResourceName generates a resource name string based on the originalName and configuration.
// If a sanitization pattern is provided in the config, originalName is first sanitized.
// If the resulting name is within MaxLength (and MaxLength > 0), it's returned.
// If MaxLength <=0, the sanitized name is returned without length restriction.
// Otherwise (name too long and MaxLength > 0), the *original* originalName is hashed, and the name is constructed as:
// <start_of_sanitized_name> + <hash_of_original_name> + <end_of_sanitized_name>,
// ensuring the total length does not exceed MaxLength.
func ConstructOCIResourceName(originalName string, config OCIResourceNameConfig) string {
	// If MaxLength is 0 or negative, it means no length limit.
	// Sanitize if a pattern is provided, then return.
	if config.MaxLength <= 0 {
		if config.InvalidCharsPattern != nil {
			return defaultSanitizeFunc(originalName, config.InvalidCharsPattern)
		}
		return originalName
	}

	// Proceed with existing logic if MaxLength is positive (MaxLength > 0)
	sanitizedName := defaultSanitizeFunc(originalName, config.InvalidCharsPattern)

	if len(sanitizedName) <= config.MaxLength {
		finalOutput := sanitizedName
		return finalOutput
	}

	// Name is too long, proceed with hashing the *original* name
	hashStr := defaultHashFunc(originalName) // 8 characters
	hashLen := len(hashStr)

	if config.MaxLength < hashLen {
		return hashStr[:config.MaxLength] // Return truncated hash if MaxLength is too small
	}
	if config.MaxLength == hashLen {
		return hashStr // Return just the hash if MaxLength is exactly hashLen
	}

	remainingSpaceForParts := config.MaxLength - hashLen
	sLen := len(sanitizedName)

	startPartLen := remainingSpaceForParts / ociResourceNameHashPartDivider
	endPartLen := remainingSpaceForParts - startPartLen

	finalStartPart := ""
	if startPartLen > 0 {
		if startPartLen >= sLen {
			finalStartPart = sanitizedName
		} else {
			finalStartPart = sanitizedName[:startPartLen]
		}
	}

	finalEndPart := calculateFinalEndPart(sLen, startPartLen, endPartLen, sanitizedName)

	var sb strings.Builder
	sb.WriteString(finalStartPart)
	sb.WriteString(hashStr)
	sb.WriteString(finalEndPart)

	finalNameResult := sb.String()

	if len(finalNameResult) > config.MaxLength { // This check should ideally not be needed if logic is perfect
		return finalNameResult[:config.MaxLength]
	}
	return finalNameResult
}
