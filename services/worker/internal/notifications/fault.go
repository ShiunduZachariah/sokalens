package notifications

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
)

// FaultKind answers the only question that matters when a send fails: is it
// safe to hand this message to a different provider?
//
// Provider independence is not "have two providers". It is knowing which
// failures are the provider's and which are the message's. Failing over on the
// wrong one either duplicates a delivery the user already received, or walks a
// malformed message through every provider in turn and dead-letters it four
// times as slowly.
type FaultKind string

const (
	// FaultUnavailable means the provider demonstrably did not accept the
	// message: it refused the connection, rejected our credentials, rate
	// limited us, or answered 5xx. Nothing was delivered, so another provider
	// may try the same message. This is the only kind that fails over.
	FaultUnavailable FaultKind = "unavailable"

	// FaultAmbiguous means we do not know whether the message was accepted --
	// a mid-flight timeout, a connection reset after the body was written, an
	// unrecognised error. Retrying the *same* provider is safe because every
	// adapter sends the outbox dedupe key as its idempotency key. Failing over
	// is not: the second provider has never heard of that key.
	FaultAmbiguous FaultKind = "ambiguous"

	// FaultPermanent means the provider read the message and rejected it --
	// an invalid recipient, a malformed body, a rejected sender. Every other
	// provider will reject it too, and attempt six will fail exactly like
	// attempt one. These are suppressed rather than retried.
	FaultPermanent FaultKind = "permanent"
)

// TransportError is the provider-neutral failure shape. Adapters translate
// their own vocabulary -- HTTP status, SMTP reply code, socket error -- into
// it once, at the edge, so that nothing downstream needs to know which
// provider is active in order to decide what to do next.
type TransportError struct {
	Provider string
	Kind     FaultKind
	Code     string
	Err      error
}

func (e *TransportError) Error() string {
	provider := e.Provider
	if provider == "" {
		provider = "email provider"
	}
	if e.Code != "" {
		return fmt.Sprintf("%s %s fault (%s): %v", provider, e.Kind, e.Code, e.Err)
	}
	return fmt.Sprintf("%s %s fault: %v", provider, e.Kind, e.Err)
}

func (e *TransportError) Unwrap() error { return e.Err }

func newTransportError(provider string, kind FaultKind, code string, err error) *TransportError {
	return &TransportError{Provider: provider, Kind: kind, Code: code, Err: err}
}

func transportFault(provider string, kind FaultKind, code, message string) *TransportError {
	return newTransportError(provider, kind, code, errors.New(message))
}

// FaultOf classifies any error. An error that never passed through an adapter
// is ambiguous by construction: an unknown failure must not be assumed safe to
// duplicate, and must not be assumed hopeless either.
func FaultOf(err error) FaultKind {
	if err == nil {
		return ""
	}
	var fault *TransportError
	if errors.As(err, &fault) {
		return fault.Kind
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return FaultAmbiguous
	}
	return FaultAmbiguous
}

// IsPermanentFault reports whether retrying can never help.
func IsPermanentFault(err error) bool { return FaultOf(err) == FaultPermanent }

// CanFailOver reports whether the message is known not to have been accepted,
// and so may be offered to the next provider in the chain.
func CanFailOver(err error) bool { return FaultOf(err) == FaultUnavailable }

// classifyHTTPStatus maps an HTTP response we actually received to a fault.
//
// Receiving *any* response means the request completed, so nothing here is
// ambiguous except an explicit 408. 401/402/403/404 are deliberately
// unavailable rather than permanent: they say this provider is misconfigured,
// suspended, or unfunded, which is precisely the situation a second provider
// exists to survive. Other 4xx mean the provider read the message and refused
// it, which no provider will do differently.
func classifyHTTPStatus(status int) FaultKind {
	switch {
	case status == 408:
		return FaultAmbiguous
	case status == 429, status >= 500:
		return FaultUnavailable
	case status == 401, status == 402, status == 403, status == 404:
		return FaultUnavailable
	case status >= 400:
		return FaultPermanent
	}
	return FaultUnavailable
}

// classifyDialError distinguishes "never reached the provider" from "reached
// it and lost the thread". Only the former is safe to fail over.
func classifyDialError(err error) FaultKind {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return FaultUnavailable
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return FaultUnavailable
	}
	return FaultAmbiguous
}

func statusCode(status int) string { return strconv.Itoa(status) }
