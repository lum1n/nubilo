//go:build !darwin

package agent

func watchLocalChanges() <-chan struct{} {
	return nil
}
