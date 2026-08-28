//go:build darwin

package agent

/*
#cgo CFLAGS: -fobjc-arc
#include "eventkit_darwin.h"
#include "contacts_darwin.h"

extern void nubilo_agent_local_changed(void);

static void nubilo_register_local_watches(void) {
	nubilo_ek_watch_changes(nubilo_agent_local_changed);
	nubilo_cn_watch_changes(nubilo_agent_local_changed);
}
*/
import "C"

import (
	"sync"
)

var (
	localChangeOnce sync.Once
	localChangeCh   = make(chan struct{}, 1)
)

//export nubilo_agent_local_changed
func nubilo_agent_local_changed() {
	select {
	case localChangeCh <- struct{}{}:
	default:
	}
}

func watchLocalChanges() <-chan struct{} {
	localChangeOnce.Do(func() {
		C.nubilo_register_local_watches()
	})
	return localChangeCh
}
