// Code generated by apigen DO NOT EDIT.

package models

import (
	"encoding/json"
	"fmt"
	"time"
)

// Unused imports workaround.
var _ = time.Time{}
var _ = json.Unmarshal
var _ = fmt.Sprint
type HealthResponsePayloadStatus string

// List of HealthResponsePayloadStatus values.
const (
	HealthResponsePayloadStatusOK HealthResponsePayloadStatus = "OK"
)

func(v HealthResponsePayloadStatus) IsOK() bool {
  return v == HealthResponsePayloadStatusOK
}

func(v HealthResponsePayloadStatus) String() string {
	return string(v)
}

type assignableHealthResponsePayloadStatus interface {
	IsOK() bool
	String() string
}

func AsHealthResponsePayloadStatus(v assignableHealthResponsePayloadStatus) (HealthResponsePayloadStatus) {
	return HealthResponsePayloadStatus(v.String())
}

func ParseHealthResponsePayloadStatus(str string, target *HealthResponsePayloadStatus) error {
	switch str {
	case "OK":
		*target = HealthResponsePayloadStatusOK
	default:
		return fmt.Errorf("unexpected HealthResponsePayloadStatus value: %s", str)
	}
	return nil
}

func (v *HealthResponsePayloadStatus) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	return ParseHealthResponsePayloadStatus(str, v)
}

// All allowed values of HealthResponsePayloadStatus enum.
var AllowableHealthResponsePayloadStatusValues = []HealthResponsePayloadStatus{
	HealthResponsePayloadStatusOK,
}

type HealthResponsePayload struct { 
	Status HealthResponsePayloadStatus `json:"status"`
}
