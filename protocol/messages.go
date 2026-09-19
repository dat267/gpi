package protocol

import (
	"fmt"
	"regexp"
)

// Port of src/protocol.ts: the validated message shapes.

// ProtocolVersion is the wire protocol version.
const ProtocolVersion = 8

// ProtocolError is an error payload.
type ProtocolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// RpcTarget fences a call: a server-wide target carries only ServerID; a
// session call additionally carries SessionID and AttachmentID.
type RpcTarget struct {
	ServerID     string  `json:"serverId"`
	SessionID    *string `json:"sessionId,omitempty"`
	AttachmentID *string `json:"attachmentId,omitempty"`
}

// IsSessionTarget reports whether the target is a session target.
func (t RpcTarget) IsSessionTarget() bool { return t.SessionID != nil }

// ClientMessage kinds.
const (
	ClientMessageHello   = "hello"
	ClientMessageRequest = "request"
	ClientMessageCancel  = "cancel"
)

// ServerMessage kinds.
const (
	ServerMessageHello         = "hello"
	ServerMessageHelloError    = "hello_error"
	ServerMessageResponse      = "response"
	ServerMessageServiceUpdate = "service_update"
	ServerMessageAttachment    = "attachment"
)

// ClientHello must be the first frame sent by a client.
type ClientHello struct {
	Version int64 `json:"version"`
}

// RequestEnvelope is an RPC request.
type RequestEnvelope struct {
	ID     string    `json:"id"`
	Target RpcTarget `json:"target"`
	Call   any       `json:"call,omitempty"`
}

// CancelEnvelope cancels an in-flight request.
type CancelEnvelope struct {
	ID     string    `json:"id"`
	Target RpcTarget `json:"target"`
}

// ClientMessage is one inbound client message.
type ClientMessage struct {
	Type    string
	Hello   *ClientHello
	Request *RequestEnvelope
	Cancel  *CancelEnvelope
}

// ServerHello acknowledges a client handshake.
type ServerHello struct {
	Version  int64  `json:"version"`
	ServerID string `json:"serverId"`
}

// ServerHelloError rejects a handshake.
type ServerHelloError struct {
	Error ProtocolError `json:"error"`
}

// ResponseEnvelope answers a request.
type ResponseEnvelope struct {
	ID     string
	OK     bool
	Result any
	Error  *ProtocolError
}

// ServiceEventEnvelope is an out-of-band service update.
type ServiceEventEnvelope struct {
	SubscriptionID string `json:"subscriptionId"`
	Update         any    `json:"update,omitempty"`
}

// AttachmentEnvelope updates the presentation's session route.
type AttachmentEnvelope struct {
	Attachment *RpcTarget `json:"attachment"`
}

// ServerMessage is one outbound server message.
type ServerMessage struct {
	Type          string
	Hello         *ServerHello
	HelloError    *ServerHelloError
	Response      *ResponseEnvelope
	ServiceUpdate *ServiceEventEnvelope
	Attachment    *AttachmentEnvelope
}

var serverIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// IsServerID validates a server id (uuid v4 shape).
func IsServerID(value string) bool { return serverIDPattern.MatchString(value) }

// ProtocolValidationError is the validation error type.
type ProtocolValidationError struct{ Message string }

func (e *ProtocolValidationError) Error() string { return e.Message }

func validationErrorf(format string, args ...any) *ProtocolValidationError {
	return &ProtocolValidationError{Message: fmt.Sprintf(format, args...)}
}

// IsSupportedProtocolVersion reports whether a version is current.
func IsSupportedProtocolVersion(version int64) bool { return version == ProtocolVersion }

// strictObjectKeys rejects unknown keys (upstream additionalProperties: false)
// and reports missing required keys.
func strictObjectKeys(value map[string]any, name string, allowed ...string) error {
	allowedSet := map[string]bool{}
	for _, key := range allowed {
		allowedSet[key] = true
	}
	for key := range value {
		if !allowedSet[key] {
			return validationErrorf("Invalid %s: unexpected property %q", name, key)
		}
	}
	return nil
}

func requireString(value map[string]any, key, name string) (string, error) {
	raw, ok := value[key]
	if !ok {
		return "", validationErrorf("Invalid %s: missing %q", name, key)
	}
	text, ok := raw.(string)
	if !ok || text == "" {
		return "", validationErrorf("Invalid %s: %q must be a non-empty string", name, key)
	}
	return text, nil
}

func requireInteger(value map[string]any, key, name string) (int64, error) {
	raw, ok := value[key]
	if !ok {
		return 0, validationErrorf("Invalid %s: missing %q", name, key)
	}
	switch typed := raw.(type) {
	case int64:
		return typed, nil
	case int:
		return int64(typed), nil
	case float64:
		if typed == float64(int64(typed)) {
			return int64(typed), nil
		}
	}
	return 0, validationErrorf("Invalid %s: %q must be an integer", name, key)
}

func parseTarget(value any, name string) (RpcTarget, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return RpcTarget{}, validationErrorf("Invalid %s: target must be an object", name)
	}
	serverID, err := requireString(object, "serverId", name+".target")
	if err != nil {
		return RpcTarget{}, err
	}
	if !IsServerID(serverID) {
		return RpcTarget{}, validationErrorf("Invalid %s: serverId must be a uuid v4", name)
	}
	target := RpcTarget{ServerID: serverID}
	sessionRaw, hasSession := object["sessionId"]
	attachmentRaw, hasAttachment := object["attachmentId"]
	switch {
	case hasSession && hasAttachment:
		sessionID, ok := sessionRaw.(string)
		if !ok || sessionID == "" {
			return RpcTarget{}, validationErrorf("Invalid %s: sessionId must be a non-empty string", name)
		}
		attachmentID, ok := attachmentRaw.(string)
		if !ok || attachmentID == "" {
			return RpcTarget{}, validationErrorf("Invalid %s: attachmentId must be a non-empty string", name)
		}
		target.SessionID = &sessionID
		target.AttachmentID = &attachmentID
	case hasSession != hasAttachment:
		return RpcTarget{}, validationErrorf("Invalid %s: session targets require both sessionId and attachmentId", name)
	}
	if err := strictObjectKeys(object, name+".target", "serverId", "sessionId", "attachmentId"); err != nil {
		return RpcTarget{}, err
	}
	return target, nil
}

// ParseClientMessage validates a decoded CBOR value as a client message.
func ParseClientMessage(value any) (*ClientMessage, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, validationErrorf("Invalid client protocol message")
	}
	messageType, err := requireString(object, "type", "client protocol message")
	if err != nil {
		return nil, err
	}
	switch messageType {
	case ClientMessageHello:
		if err := strictObjectKeys(object, "client hello", "type", "version"); err != nil {
			return nil, err
		}
		version, err := requireInteger(object, "version", "client hello")
		if err != nil {
			return nil, err
		}
		if version < 0 {
			return nil, validationErrorf("Invalid client hello: version must be >= 0")
		}
		return &ClientMessage{Type: ClientMessageHello, Hello: &ClientHello{Version: version}}, nil
	case ClientMessageRequest:
		if err := strictObjectKeys(object, "client request", "type", "id", "target", "call"); err != nil {
			return nil, err
		}
		id, err := requireString(object, "id", "client request")
		if err != nil {
			return nil, err
		}
		target, err := parseTarget(object["target"], "client request")
		if err != nil {
			return nil, err
		}
		return &ClientMessage{Type: ClientMessageRequest,
			Request: &RequestEnvelope{ID: id, Target: target, Call: object["call"]}}, nil
	case ClientMessageCancel:
		if err := strictObjectKeys(object, "client cancel", "type", "id", "target"); err != nil {
			return nil, err
		}
		id, err := requireString(object, "id", "client cancel")
		if err != nil {
			return nil, err
		}
		target, err := parseTarget(object["target"], "client cancel")
		if err != nil {
			return nil, err
		}
		return &ClientMessage{Type: ClientMessageCancel, Cancel: &CancelEnvelope{ID: id, Target: target}}, nil
	default:
		return nil, validationErrorf("Invalid client protocol message: unknown type %q", messageType)
	}
}

// ParseServerMessage validates a decoded CBOR value as a server message.
func ParseServerMessage(value any) (*ServerMessage, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, validationErrorf("Invalid server protocol message")
	}
	messageType, err := requireString(object, "type", "server protocol message")
	if err != nil {
		return nil, err
	}
	switch messageType {
	case ServerMessageHello:
		if err := strictObjectKeys(object, "server hello", "type", "version", "serverId"); err != nil {
			return nil, err
		}
		version, err := requireInteger(object, "version", "server hello")
		if err != nil {
			return nil, err
		}
		if !IsSupportedProtocolVersion(version) {
			return nil, validationErrorf("Invalid server hello: unsupported protocol version")
		}
		serverID, err := requireString(object, "serverId", "server hello")
		if err != nil {
			return nil, err
		}
		if !IsServerID(serverID) {
			return nil, validationErrorf("Invalid server hello: serverId must be a uuid v4")
		}
		return &ServerMessage{Type: ServerMessageHello, Hello: &ServerHello{Version: version, ServerID: serverID}}, nil
	case ServerMessageHelloError:
		if err := strictObjectKeys(object, "server hello_error", "type", "error"); err != nil {
			return nil, err
		}
		protocolError, err := parseProtocolError(object["error"], "server hello_error")
		if err != nil {
			return nil, err
		}
		return &ServerMessage{Type: ServerMessageHelloError, HelloError: &ServerHelloError{Error: protocolError}}, nil
	case ServerMessageResponse:
		raw, ok := object["ok"]
		if !ok {
			return nil, validationErrorf("Invalid server response: missing %q", "ok")
		}
		okValue, ok := raw.(bool)
		if !ok {
			return nil, validationErrorf("Invalid server response: %q must be a boolean", "ok")
		}
		if okValue {
			if err := strictObjectKeys(object, "server response", "type", "id", "ok", "result"); err != nil {
				return nil, err
			}
			id, err := requireString(object, "id", "server response")
			if err != nil {
				return nil, err
			}
			return &ServerMessage{Type: ServerMessageResponse,
				Response: &ResponseEnvelope{ID: id, OK: true, Result: object["result"]}}, nil
		}
		if err := strictObjectKeys(object, "server response", "type", "id", "ok", "error"); err != nil {
			return nil, err
		}
		id, err := requireString(object, "id", "server response")
		if err != nil {
			return nil, err
		}
		protocolError, err := parseProtocolError(object["error"], "server response")
		if err != nil {
			return nil, err
		}
		return &ServerMessage{Type: ServerMessageResponse,
			Response: &ResponseEnvelope{ID: id, OK: false, Error: &protocolError}}, nil
	case ServerMessageServiceUpdate:
		if err := strictObjectKeys(object, "server service_update", "type", "subscriptionId", "update"); err != nil {
			return nil, err
		}
		subscriptionID, err := requireString(object, "subscriptionId", "server service_update")
		if err != nil {
			return nil, err
		}
		return &ServerMessage{Type: ServerMessageServiceUpdate,
			ServiceUpdate: &ServiceEventEnvelope{SubscriptionID: subscriptionID, Update: object["update"]}}, nil
	case ServerMessageAttachment:
		if err := strictObjectKeys(object, "server attachment", "type", "attachment"); err != nil {
			return nil, err
		}
		raw, ok := object["attachment"]
		if !ok {
			return nil, validationErrorf("Invalid server attachment: missing %q", "attachment")
		}
		if raw == nil {
			return &ServerMessage{Type: ServerMessageAttachment, Attachment: &AttachmentEnvelope{Attachment: nil}}, nil
		}
		target, err := parseTarget(raw, "server attachment")
		if err != nil {
			return nil, err
		}
		return &ServerMessage{Type: ServerMessageAttachment, Attachment: &AttachmentEnvelope{Attachment: &target}}, nil
	default:
		return nil, validationErrorf("Invalid server protocol message: unknown type %q", messageType)
	}
}

func parseProtocolError(value any, name string) (ProtocolError, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return ProtocolError{}, validationErrorf("Invalid %s: error must be an object", name)
	}
	if err := strictObjectKeys(object, name+".error", "code", "message"); err != nil {
		return ProtocolError{}, err
	}
	code, err := requireString(object, "code", name+".error")
	if err != nil {
		return ProtocolError{}, err
	}
	rawMessage, ok := object["message"]
	if !ok {
		return ProtocolError{}, validationErrorf("Invalid %s: error.message is required", name)
	}
	message, ok := rawMessage.(string)
	if !ok {
		return ProtocolError{}, validationErrorf("Invalid %s: error.message must be a string", name)
	}
	return ProtocolError{Code: code, Message: message}, nil
}

// ClientHelloWire / ServerHelloWire and friends produce the CBOR value shapes.

// ClientMessageValue renders a client message as the CBOR value shape.
func ClientMessageValue(message *ClientMessage) (map[string]any, error) {
	switch message.Type {
	case ClientMessageHello:
		return map[string]any{"type": "hello", "version": message.Hello.Version}, nil
	case ClientMessageRequest:
		target, err := targetValue(message.Request.Target)
		if err != nil {
			return nil, err
		}
		value := map[string]any{"type": "request", "id": message.Request.ID, "target": target}
		if message.Request.Call != nil {
			value["call"] = message.Request.Call
		}
		return value, nil
	case ClientMessageCancel:
		target, err := targetValue(message.Cancel.Target)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "cancel", "id": message.Cancel.ID, "target": target}, nil
	default:
		return nil, validationErrorf("unknown client message type %q", message.Type)
	}
}

// ServerMessageValue renders a server message as the CBOR value shape.
func ServerMessageValue(message *ServerMessage) (map[string]any, error) {
	switch message.Type {
	case ServerMessageHello:
		return map[string]any{"type": "hello", "version": message.Hello.Version, "serverId": message.Hello.ServerID}, nil
	case ServerMessageHelloError:
		return map[string]any{"type": "hello_error", "error": protocolErrorValue(message.HelloError.Error)}, nil
	case ServerMessageResponse:
		response := message.Response
		if response.OK {
			value := map[string]any{"type": "response", "id": response.ID, "ok": true}
			if response.Result != nil {
				value["result"] = response.Result
			}
			return value, nil
		}
		if response.Error == nil {
			return nil, validationErrorf("failed response requires an error")
		}
		return map[string]any{"type": "response", "id": response.ID, "ok": false,
			"error": protocolErrorValue(*response.Error)}, nil
	case ServerMessageServiceUpdate:
		value := map[string]any{"type": "service_update", "subscriptionId": message.ServiceUpdate.SubscriptionID}
		if message.ServiceUpdate.Update != nil {
			value["update"] = message.ServiceUpdate.Update
		}
		return value, nil
	case ServerMessageAttachment:
		if message.Attachment.Attachment == nil {
			return map[string]any{"type": "attachment", "attachment": nil}, nil
		}
		target, err := targetValue(*message.Attachment.Attachment)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "attachment", "attachment": target}, nil
	default:
		return nil, validationErrorf("unknown server message type %q", message.Type)
	}
}

func targetValue(target RpcTarget) (map[string]any, error) {
	if !IsServerID(target.ServerID) {
		return nil, validationErrorf("serverId must be a uuid v4")
	}
	value := map[string]any{"serverId": target.ServerID}
	if target.SessionID != nil {
		if target.AttachmentID == nil {
			return nil, validationErrorf("session targets require both sessionId and attachmentId")
		}
		value["sessionId"] = *target.SessionID
		value["attachmentId"] = *target.AttachmentID
	}
	return value, nil
}

func protocolErrorValue(protocolError ProtocolError) map[string]any {
	return map[string]any{"code": protocolError.Code, "message": protocolError.Message}
}
