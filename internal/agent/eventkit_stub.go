//go:build !darwin || !cgo

package agent

// OpenCalendars returns ErrNeedDarwin: calendar mirroring reads EventKit.
func OpenCalendars() (CalendarSource, error) { return nil, ErrNeedDarwin }

// PlatformCalendars returns ErrNeedDarwin off macOS.
func PlatformCalendars() ([]CalendarInfo, error) { return nil, ErrNeedDarwin }
