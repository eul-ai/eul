package backend

import "context"

type LoginMethod string

const (
	LoginBrowser LoginMethod = "browser"
	LoginDevice  LoginMethod = "device"
)

type DeviceCode struct {
	VerificationURL string
	UserCode        string
}

type LoginInteraction struct {
	AuthURL    func(string) error
	DeviceCode func(DeviceCode) error
}

type Authenticator interface {
	Login(context.Context, LoginMethod, LoginInteraction) error
	Logout(context.Context) error
}
