package domain

// RequestError marks an explicit harness rejection of completion parameters.
// Adapters must classify structured protocol errors, never message substrings.
// The cause remains available to local diagnostics, not the public response.
type RequestError struct{ Cause error }

func (e *RequestError) Error() string { return e.Cause.Error() }
func (e *RequestError) Unwrap() error { return e.Cause }
