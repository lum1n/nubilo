//go:build darwin

package setup

import (
	"path/filepath"

	"nubilo/internal/agent"
)

func agentAppBundle() (string, error) {
	appPath, err := agent.InstallAppBundle()
	if err != nil {
		return "", err
	}
	return filepath.Join(appPath, "Contents", "MacOS", "nubilo"), nil
}
