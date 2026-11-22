package domain

import (
	"errors"
	"fmt"
)

// Error kinds. The HTTP layer maps each one to a status code; wrap them with
// %w to add context.
var (
	ErrNotFound               = errors.New("not found")
	ErrInvalidInput           = errors.New("invalid input")
	ErrConflict               = errors.New("conflict")
	ErrUnauthorized           = errors.New("unauthorized")
	ErrInvalidTransition      = errors.New("invalid status transition")
	ErrUnsupported            = errors.New("unsupported")
	ErrInvalidSignature       = errors.New("invalid signature")
	ErrProvider               = errors.New("payment provider error")
	ErrConcurrentModification = errors.New("concurrent modification")
)

// ValidationError names the invalid field. It matches ErrInvalidInput.
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

func Invalid(field, message string) error {
	return &ValidationError{Field: field, Message: message}
}
