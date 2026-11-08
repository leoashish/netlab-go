// Package todo holds the helper every unfinished stub uses.
package todo

import (
	"errors"
	"fmt"
)

// ErrNotImplemented marks an unfinished stub. Tests use it to tell "your code returned an error on purpose" from
// "you have not written this yet".
var ErrNotImplemented = errors.New("not implemented yet")

// NotImplemented is what an unfinished stub returns. Stubs return this error (instead of panicking) so a failing test
// tells you which chapter is unfinished, and one unfinished function cannot crash the whole test binary.
func NotImplemented(what string) error { return fmt.Errorf("%w: %s", ErrNotImplemented, what) }
