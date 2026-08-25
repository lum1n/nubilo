//go:build !darwin

package agent

func PlatformCalendars() ([]CalendarInfo, error) {
	return nil, ErrNeedDarwin
}

func PlatformReminderLists() ([]CalendarInfo, error) {
	return nil, ErrNeedDarwin
}

func OpenPlatform(sel Selection) (CalendarSource, ContactSource, PhotoSource, ReminderSource, error) {
	return nil, nil, nil, nil, ErrNeedDarwin
}

func PlatformAlbums() ([]PhotoInfo, error) {
	return nil, ErrNeedDarwin
}

func PlatformAlbumList() (int, []PhotoInfo, error) {
	return 0, nil, ErrNeedDarwin
}
