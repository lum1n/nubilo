//go:build !darwin || !cgo

package agent

func watchLocalChanges() <-chan struct{} {
	return nil
}
