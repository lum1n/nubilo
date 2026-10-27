package agent

import "errors"

var (
	ErrNeedDarwin = errors.New("agent: PhotoKit requires macOS")
	ErrNotPaired  = errors.New("agent: device is not paired (run nubilo pair --server …)")
)

type PhotoInfo struct {
	ID    string
	Title string
	Kind  string // user|smart|shared|synced|person|pet
	Count int    // assets visible under current Photos authorization
}

type LocalPhoto struct {
	ID         string
	Filename   string
	Kind       string // image | video | live | raw
	TakenAtMS  int64
	ModMS      int64
	DurationMS int64
	Size       int64
	Albums     []string
	Original   []byte
	LiveMovie  []byte // paired Live Photo movie when Kind=live
}

type PhotoFilter struct {
	Source   string
	Albums   []string
	AfterMS  int64
	BeforeMS int64
}

type PhotoSource interface {
	ListAlbums() ([]PhotoInfo, error)
	ListPhotos(filter PhotoFilter) ([]LocalPhoto, error)
	ReadOriginal(id string) ([]byte, error)
	ReadLiveMovie(id string) ([]byte, error) // nil,nil if not a Live Photo
	ImportOriginal(data []byte, filename, albumID string) (localID string, err error)
}
