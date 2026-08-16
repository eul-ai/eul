package oauth

func readCredentials(path string) (credentials, error) {
	return (&credentialStore{path: path, sleep: sleepContext}).read()
}

func writeCredentials(path string, credential credentials) error {
	return (&credentialStore{path: path, sleep: sleepContext}).write(credential)
}
