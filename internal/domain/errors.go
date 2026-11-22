package domain

import (
	"errors"
	"fmt"
)

// Sentinel errors describe *what kind* of failure happened, independent of
// transport. The HTTP layer maps each kind to a status code in one place.
//
// Wrap them with fmt.Errorf("...: %w", ErrX) to add context; callers check with
// errors.Is.
var (
	ErrNotFound          = errors.New("not found")
	ErrInvalidInput      = errors.New("invalid input")
	ErrConflict          = errors.New("conflict")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrInvalidTransition = errors.New("invalid status transition")
	ErrUnsupported       = errors.New("unsupported")
	ErrInvalidSignature  = errors.New("invalid signature")
	ErrProvider          = errors.New("payment provider error")
)

// ValidationError reports which field is wrong and why. It matches
// ErrInvalidInput via errors.Is.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e *ValidationError) Is(target error) bool {
	return target == ErrInvalidInput
}

// Invalid is a short constructor for ValidationError.
func Invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
