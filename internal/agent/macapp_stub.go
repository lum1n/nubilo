//go:build !darwin

package agent

import "errors"

func PhotosAuthStatus() string { return "unsupported" }

func RequestPhotosAccess() (string, error) {
	return "", ErrNeedDarwin
}

func InstallAppBundle() (string, error) {
	return "", ErrNeedDarwin
}

func AuthorizePhotosViaApp() (string, error) {
	return "", ErrNeedDarwin
}

func OpenPhotosPrivacySettings() error {
	return errors.New("agent: Photos privacy settings require macOS")
}
