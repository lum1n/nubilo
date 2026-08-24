//go:build darwin

package agent

func OpenPlatform(sel Selection) (CalendarSource, ContactSource, PhotoSource, ReminderSource, error) {
	cal, err := openEventKit()
	if err != nil {
		return nil, nil, nil, nil, err
	}
	var book ContactSource
	if sel.SyncContacts {
		c, err := openContacts()
		if err != nil {
			return cal, nil, nil, cal, err
		}
		book = c
	}
	var pics PhotoSource
	if sel.Photos.Enabled {
		p, err := openPhotoKit()
		if err != nil {
			return cal, book, nil, cal, err
		}
		pics = p
	}
	return cal, book, pics, cal, nil
}

func PlatformCalendars() ([]CalendarInfo, error) {
	ek, err := openEventKit()
	if err != nil {
		return nil, err
	}
	return ek.ListCalendars()
}

func PlatformReminderLists() ([]CalendarInfo, error) {
	ek, err := openEventKit()
	if err != nil {
		return nil, err
	}
	return ek.ListReminderLists()
}

func PlatformAlbums() ([]PhotoInfo, error) {
	pk, err := openPhotoKit()
	if err != nil {
		return nil, err
	}
	return pk.ListAlbums()
}
