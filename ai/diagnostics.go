package ai

import (
	"encoding/json"
	"fmt"
	"time"
)

// Port of utils/diagnostics.ts.

// DiagnosticErrorInfo describes a thrown provider/runtime error, redacted.
type DiagnosticErrorInfo struct {
	Name    *string         `json:"name,omitempty"`
	Message string          `json:"message"`
	Stack   *string         `json:"stack,omitempty"`
	Code    json.RawMessage `json:"code,omitempty"` // string | number
}

// AssistantMessageDiagnostic is a redacted provider/runtime diagnostic for
// failures and recoveries.
type AssistantMessageDiagnostic struct {
	Type      string               `json:"type"`
	Timestamp int64                `json:"timestamp"`
	Error     *DiagnosticErrorInfo `json:"error,omitempty"`
	Details   json.RawMessage      `json:"details,omitempty"` // JsonObject
}

// FormatThrownValue renders a thrown value the way upstream does: an error's
// message (or its name when the message is empty), or String(value).
func FormatThrownValue(err error) string {
	if err == nil {
		return "undefined"
	}
	msg := err.Error()
	if msg == "" {
		return fmt.Sprintf("%T", err)
	}
	return msg
}

// ExtractDiagnosticError builds a DiagnosticErrorInfo from a Go error.
// Go errors have no name/stack split like JS Errors, so Name is derived from
// the error's dynamic type when it implements Namer, and Stack from a
// StackFormatter.
type Namer interface{ ErrorName() string }
type StackFormatter interface{ ErrorStack() string }

func ExtractDiagnosticError(err error) DiagnosticErrorInfo {
	if err == nil {
		name := "ThrownValue"
		return DiagnosticErrorInfo{Name: &name, Message: "undefined"}
	}
	info := DiagnosticErrorInfo{Message: FormatThrownValue(err)}
	if n, ok := err.(Namer); ok {
		if name := n.ErrorName(); name != "" {
			info.Name = &name
		}
	} else {
		name := fmt.Sprintf("%T", err)
		info.Name = &name
	}
	if sf, ok := err.(StackFormatter); ok {
		if stack := sf.ErrorStack(); stack != "" {
			info.Stack = &stack
		}
	}
	if coder, ok := err.(interface{ ErrorCode() any }); ok {
		switch code := coder.ErrorCode().(type) {
		case string:
			info.Code, _ = json.Marshal(code)
		case int:
			info.Code, _ = json.Marshal(code)
		case int64:
			info.Code, _ = json.Marshal(code)
		case float64:
			info.Code, _ = json.Marshal(code)
		}
	}
	return info
}

// CreateAssistantMessageDiagnostic builds a diagnostic stamped with the
// current time.
func CreateAssistantMessageDiagnostic(typ string, err error, details json.RawMessage) AssistantMessageDiagnostic {
	return AssistantMessageDiagnostic{
		Type:      typ,
		Timestamp: time.Now().UnixMilli(),
		Error:     ptrDiagnosticError(ExtractDiagnosticError(err)),
		Details:   details,
	}
}

func ptrDiagnosticError(e DiagnosticErrorInfo) *DiagnosticErrorInfo { return &e }

// AppendAssistantMessageDiagnostic appends to a message's diagnostics.
func AppendAssistantMessageDiagnostic(message *AssistantMessage, diagnostic AssistantMessageDiagnostic) {
	message.Diagnostics = append(message.Diagnostics, diagnostic)
}
