package server

// Port of src/errors.ts: host and lifecycle errors that may cross the protocol
// boundary.

// Server error codes.
const (
	ErrWrongServer        = "wrong_server"
	ErrSessionNotFound    = "session_not_found"
	ErrSessionAmbiguous   = "session_ambiguous"
	ErrSessionNotAttached = "session_not_attached"
	ErrServerDraining     = "server_draining"
)

// InternalServerErrorMessage is the message sent in place of an unexpected
// error's details.
const InternalServerErrorMessage = "Internal server error"

// ServerError is a host or lifecycle error that can safely cross the protocol
// boundary.
type ServerError struct {
	Code    string
	Message string
}

func (e *ServerError) Error() string { return e.Message }

// ServerCode exposes the code through an interface so wrapper types that embed
// ServerError are recognised by ServerErrorCode.
func (e *ServerError) ServerCode() string { return e.Code }

// WrongServerError reports a request addressed to another server.
type WrongServerError struct{ ServerError }

// NewWrongServerError builds the error.
func NewWrongServerError() *WrongServerError {
	return &WrongServerError{ServerError{Code: ErrWrongServer, Message: "Request was addressed to another server"}}
}

// SessionNotFoundError reports an unknown session.
type SessionNotFoundError struct{ ServerError }

// NewSessionNotFoundError builds the error.
func NewSessionNotFoundError(message string) *SessionNotFoundError {
	if message == "" {
		message = "Session was not found"
	}
	return &SessionNotFoundError{ServerError{Code: ErrSessionNotFound, Message: message}}
}

// SessionAmbiguousError reports an id matching several sessions.
type SessionAmbiguousError struct{ ServerError }

// NewSessionAmbiguousError builds the error.
func NewSessionAmbiguousError() *SessionAmbiguousError {
	return &SessionAmbiguousError{ServerError{Code: ErrSessionAmbiguous, Message: "Session ID matches more than one session"}}
}

// SessionNotAttachedError reports a call to a session this client is not
// attached to.
type SessionNotAttachedError struct{ ServerError }

// NewSessionNotAttachedError builds the error.
func NewSessionNotAttachedError() *SessionNotAttachedError {
	return &SessionNotAttachedError{ServerError{Code: ErrSessionNotAttached, Message: "Session is not attached to this client"}}
}

// ServerDrainingError reports work refused while the server is closing.
type ServerDrainingError struct{ ServerError }

// NewServerDrainingError builds the error.
func NewServerDrainingError() *ServerDrainingError {
	return &ServerDrainingError{ServerError{Code: ErrServerDraining, Message: "Server is draining"}}
}

// codedError is implemented by ServerError and every type embedding it.
type codedError interface {
	ServerCode() string
}

// ServerErrorCode extracts a bounded protocol code from an error, when it
// carries one.
func ServerErrorCode(err error) (string, bool) {
	for err != nil {
		if coded, ok := err.(codedError); ok {
			return coded.ServerCode(), true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return "", false
		}
		err = unwrapper.Unwrap()
	}
	return "", false
}
