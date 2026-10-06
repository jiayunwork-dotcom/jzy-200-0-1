// Package application wires the domain packages to the store: validation,
// the append-only inspection-record version store, plan projection,
// historical queries and material-update impact analysis.
package app

import (
	"errors"
	"strings"
)

// FieldError names the offending request field.
type FieldError struct {
	Field  string `json:"field"`
	Reason string `json:"reason"`
}

func (e FieldError) Error() string { return e.Field + ": " + e.Reason }

// FieldErrors is the full list of rejected fields.
type FieldErrors []FieldError

func (e FieldErrors) Error() string {
	parts := make([]string, len(e))
	for i, f := range e {
		parts[i] = f.Error()
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// AsFieldErrors extracts a FieldErrors list from err if present.
func AsFieldErrors(err error) (FieldErrors, bool) {
	var fe FieldErrors
	if errors.As(err, &fe) {
		return fe, true
	}
	return nil, false
}

func addField(errs *FieldErrors, field, reason string) {
	*errs = append(*errs, FieldError{Field: field, Reason: reason})
}
