package oauth

import "context"

// Authenticator is implemented by runtimes that support Codex OAuth login and logout.
type Authenticator interface {
	Login(context.Context, LoginMethod, Interaction) error
	Logout(context.Context) error
}
