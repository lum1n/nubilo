//go:build !darwin

package agent

func OpenPlatform() (PhotoSource, error) {
	return nil, ErrNeedDarwin
}

func PlatformAlbums() ([]PhotoInfo, error) {
	return nil, ErrNeedDarwin
}

func PlatformAlbumList() (int, []PhotoInfo, error) {
	return 0, nil, ErrNeedDarwin
}
