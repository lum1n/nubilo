package agent

import "errors"

var ErrNotPaired = errors.New("agent: device is not paired (run nubilo pair --server …)")
