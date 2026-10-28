package responses

func (c *Client) errorf(format string, arguments ...any) error {
	return c.errorConfig.Errorf(format, arguments...)
}

func (c *Client) wrapf(cause error, format string, arguments ...any) error {
	return c.errorConfig.Wrapf(cause, format, arguments...)
}
