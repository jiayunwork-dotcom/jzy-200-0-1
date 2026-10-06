package store

import "strconv"

// ErrAlreadyExists indicates a uniqueness violation.
type ErrAlreadyExists string

func (e ErrAlreadyExists) Error() string { return string(e) + " already exists" }

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Compile-time assertions that the implementations satisfy the interfaces.
var (
	_ Store = (*Memory)(nil)
	_ Tx    = (*memTx)(nil)
)
