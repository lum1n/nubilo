//go:build !darwin

package setup

func agentAppBundle() (string, error) {
	return "", nil
}
