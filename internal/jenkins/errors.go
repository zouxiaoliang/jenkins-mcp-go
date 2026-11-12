package jenkins

import (
	"errors"
	"fmt"
	"net"
	"strings"
)

// ErrorCode is the finite set of failures the bridge distinguishes.
//
// The set is deliberately closed: an agent must be able to decide whether a
// retry is worthwhile without parsing English prose.
type ErrorCode string

const (
	CodeNotFound              ErrorCode = "not_found"
	CodeForbidden             ErrorCode = "forbidden"
	CodeAuthFailed            ErrorCode = "auth_failed"
	CodeCSRFFailed            ErrorCode = "csrf_failed"
	CodeWriteDisabled         ErrorCode = "write_disabled"
	CodeInvalidParameter      ErrorCode = "invalid_parameter"
	CodeJobHasNoParameters    ErrorCode = "job_has_no_parameters"
	CodeQueueTimeout          ErrorCode = "queue_timeout"
	CodeControllerUnreachable ErrorCode = "controller_unreachable"
	CodeBadGateway            ErrorCode = "bad_gateway"
	CodeBadRequest            ErrorCode = "bad_request"
	CodeInternal              ErrorCode = "internal"
)

// retryable reports whether repeating the same call could plausibly succeed.
func (c ErrorCode) retryable() bool {
	switch c {
	case CodeQueueTimeout, CodeControllerUnreachable, CodeBadGateway, CodeInternal:
		return true
	}
	return false
}

// Error is the bridge's error contract. Every tool failure is rendered from it,
// so that retryability is stated rather than inferred from an HTTP status.
type Error struct {
	Code      ErrorCode `json:"code"`
	Message   string    `json:"message"`
	Retryable bool      `json:"retryable"`
	Detail    string    `json:"detail,omitempty"`
	err       error
}

func (e *Error) Error() string {
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s (%s)", e.Code, e.Message, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error { return e.err }

// NewError builds an Error, deriving Retryable from the code so that the two can
// never disagree.
func NewError(code ErrorCode, msg string, detail string) *Error {
	return &Error{Code: code, Message: msg, Retryable: code.retryable(), Detail: detail}
}

// Wrap attaches a cause to an Error without changing its code.
func (e *Error) Wrap(err error) *Error {
	e.err = err
	return e
}

// AsError extracts the contract error from an arbitrary error chain.
func AsError(err error) *Error {
	var je *Error
	if errors.As(err, &je) {
		return je
	}
	if err == nil {
		return nil
	}
	// A transport failure that was not classified on the way out is, by
	// definition, a plumbing problem the caller may retry.
	return NewError(CodeInternal, err.Error(), "")
}

// classifyHTTP maps a status code to the contract, using the response body only
// as detail. 404 and 403 must stay distinct: "absent" and "not permitted" mean
// different things to an agent, and conflating them is how a permission bug
// hides as an empty list.
func classifyHTTP(status int, body string) *Error {
	detail := trimBody(body)
	switch status {
	case 400:
		return NewError(CodeBadRequest, "controller rejected the request", detail)
	case 401:
		return NewError(CodeAuthFailed, "controller rejected the credentials", detail)
	case 403:
		return NewError(CodeForbidden, "not permitted by the controller", detail)
	case 404:
		return NewError(CodeNotFound, "not found on the controller", detail)
	case 502, 503, 504:
		return NewError(CodeBadGateway, "controller is unavailable", detail)
	}
	if status >= 500 {
		return NewError(CodeBadGateway, fmt.Sprintf("controller returned HTTP %d", status), detail)
	}
	return NewError(CodeBadRequest, fmt.Sprintf("controller returned HTTP %d", status), detail)
}

// classifyTransport maps a dial/timeout failure to controller_unreachable so the
// agent retries instead of concluding the object does not exist.
func classifyTransport(err error) *Error {
	var nerr net.Error
	if errors.As(err, &nerr) && nerr.Timeout() {
		return NewError(CodeControllerUnreachable, "request to the controller timed out", err.Error()).Wrap(err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return NewError(CodeControllerUnreachable, "controller host could not be resolved", dnsErr.Error()).Wrap(err)
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return NewError(CodeControllerUnreachable, "controller could not be reached", opErr.Error()).Wrap(err)
	}
	return NewError(CodeControllerUnreachable, "controller could not be reached", err.Error()).Wrap(err)
}

func trimBody(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 400 {
		return s[:400] + "..."
	}
	return s
}
