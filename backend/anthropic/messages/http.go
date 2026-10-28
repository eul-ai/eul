package messages

func (client *Client) errorf(format string, arguments ...any) error {
	return client.errorConfig.Errorf(format, arguments...)
}

func (client *Client) wrapf(cause error, format string, arguments ...any) error {
	return client.errorConfig.Wrapf(cause, format, arguments...)
}
