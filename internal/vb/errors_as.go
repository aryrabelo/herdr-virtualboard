package vb

import "errors"

// errorsAs is a thin alias so command helpers read without repeating the
// errors.As ceremony on every branch.
func errorsAs(err error, target **Error) bool { return errors.As(err, target) }
