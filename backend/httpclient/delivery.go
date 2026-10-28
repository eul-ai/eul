package httpclient

import "errors"

type ObserverError struct {
	operation string
	cause     error
}

func (err *ObserverError) Error() string { return err.operation + ": " + err.cause.Error() }
func (err *ObserverError) Unwrap() error { return err.cause }

func NewObserverError(operation string, cause error) error {
	return &ObserverError{operation: operation, cause: cause}
}

type DeliveryTracker struct{}

func (DeliveryTracker) Deliver(operation string, deliver func() error) error {
	if err := deliver(); err != nil {
		return NewObserverError(operation, err)
	}
	return nil
}

func IsObserverError(err error) bool {
	var observerErr *ObserverError
	return errors.As(err, &observerErr)
}
