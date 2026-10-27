//go:build darwin

package agent

// OpenPlatform opens Photos.app. It asks for no permission; the agent only
// reads the library while photos are turned on in the selection.
func OpenPlatform() (PhotoSource, error) {
	return openPhotoKit()
}

func PlatformAlbums() ([]PhotoInfo, error) {
	_, albums, err := PlatformAlbumList()
	return albums, err
}
