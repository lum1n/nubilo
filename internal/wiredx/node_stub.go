//go:build !wired

package wiredx

// Available reports whether this binary includes wired support.
func Available() bool { return false }

func Join(JoinOptions) (Node, error) { return nil, ErrUnavailable }

func Anchor(AnchorOptions) (Node, error) { return nil, ErrUnavailable }
