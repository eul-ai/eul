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

type PartialResponseError struct {
	cause error
}

func (err *PartialResponseError) Error() string { return err.cause.Error() }
func (err *PartialResponseError) Unwrap() error { return err.cause }

func NewPartialResponseError(cause error) error {
	return &PartialResponseError{cause: cause}
}

type DeliveryTracker struct {
	observed bool
}

func (tracker *DeliveryTracker) Deliver(operation string, deliver func() error) error {
	if err := deliver(); err != nil {
		return NewObserverError(operation, err)
	}
	tracker.observed = true
	return nil
}

func (tracker *DeliveryTracker) Observed() bool {
	return tracker.observed
}

func (tracker *DeliveryTracker) WrapPartial(err error) error {
	if err == nil || !tracker.observed || IsObserverError(err) {
		return err
	}
	return NewPartialResponseError(err)
}

func IsObserverError(err error) bool {
	var observerErr *ObserverError
	return errors.As(err, &observerErr)
}

func IsPartialResponseError(err error) bool {
	var partialErr *PartialResponseError
	return errors.As(err, &partialErr)
}

func IsNonRetryableStreamError(err error) bool {
	return IsObserverError(err) || IsPartialResponseError(err)
}
