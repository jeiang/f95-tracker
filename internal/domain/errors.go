package domain

import "errors"

// Sentinels wrapped with %w by services; web maps them to 404/409/409/422.
var (
	ErrNotFound          = errors.New("not found")
	ErrConflict          = errors.New("conflict")
	ErrIllegalTransition = errors.New("illegal transition")
	ErrValidation        = errors.New("validation failed")
)
