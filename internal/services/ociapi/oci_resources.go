package ociapi

import (
	"fmt"
	"hash/crc32"
	"regexp"
)

const (
	ociResourceNameHashPartDivider = 2
)

// OCIResourceNameConfig holds configuration for generating OCI resource names.
type OCIResourceNameConfig struct {
	MaxLength           int
	InvalidCharsPattern *regexp.Regexp
	sanitizeFunc        func(string, *regexp.Regexp) string
	hashFunc            func(string) string
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
	if config.hashFunc == nil {
		config.hashFunc = defaultHashFunc
	}

	if config.sanitizeFunc == nil {
		config.sanitizeFunc = defaultSanitizeFunc
	}

	resultingName := originalName
	if config.InvalidCharsPattern != nil {
		resultingName = config.sanitizeFunc(originalName, config.InvalidCharsPattern)
	}

	const hashPartDivider = 2

	if config.MaxLength > 0 && len(resultingName) > config.MaxLength {
		hash := config.hashFunc(originalName)
		if len(hash) >= config.MaxLength {
			return hash[:config.MaxLength]
		}
		originalLength := len(resultingName)
		hashStarts := originalLength/hashPartDivider - len(hash)/hashPartDivider
		hashEnds := hashStarts + len(hash)
		remainingLength := config.MaxLength - hashEnds
		resultingName = resultingName[:hashStarts] + hash + resultingName[originalLength-remainingLength:]
	}

	return resultingName
}
