//go:build !darwin

package agent

func PlatformCalendars() ([]CalendarInfo, error) {
	return nil, ErrNeedDarwin
}

func OpenPlatform(sel Selection) (CalendarSource, ContactSource, PhotoSource, error) {
	return nil, nil, nil, ErrNeedDarwin
}

func PlatformAlbums() ([]PhotoInfo, error) {
	return nil, ErrNeedDarwin
}
