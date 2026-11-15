package plugins

import "errors"

const (
	CodeInvalidRequest    = "invalid_request"
	CodeProtocolMismatch  = "protocol_mismatch"
	CodeUnsupportedAction = "unsupported_action"
	CodeUnsupportedURL    = "unsupported_url"
	CodeDownloadFailed    = "download_failed"
	CodeWriteFailed       = "write_failed"
	CodeCancelled         = "cancelled"
	CodeInternal          = "internal"
)

// Failure is a host or plugin failure. Error text is safe to log: it does
// not include the import URL. Stderr is capped and redacted the same way.
type Failure struct {
	Code     string
	Message  string
	PluginID string
	JobID    string
	ExitCode int
	Stderr   string
	cause    error
}

func (f *Failure) Error() string {
	if f == nil {
		return ""
	}
	return f.Message
}

func (f *Failure) Unwrap() error { return f.cause }

// IsCode reports whether err is a Failure with the given protocol code.
func IsCode(err error, code string) bool {
	var failure *Failure
	if !errors.As(err, &failure) {
		return false
	}
	return failure.Code == code
}

func failure(code, message string) *Failure {
	return &Failure{Code: code, Message: message, ExitCode: -1}
}

// ManifestError is a manifest or discovery document that failed validation.
type ManifestError struct {
	Message string
}

func (e *ManifestError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func manifestError(message string) error {
	return &ManifestError{Message: message}
}
