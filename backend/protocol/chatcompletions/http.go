package chatcompletions

func (client *Client) errorf(format string, arguments ...any) error {
	return client.errorConfig.Errorf(format, arguments...)
}
