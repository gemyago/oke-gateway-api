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

type EchoResponsePayload struct { 
	Message string `json:"message"`
}
