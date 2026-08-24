package syncengine

import (
	"encoding/json"
	"errors"
)

var (
	ErrNotFound      = errors.New("sync: not found")
	ErrConflict      = errors.New("sync: conflict")
	ErrAuthorization = errors.New("sync: unauthorized")
	ErrStaleCursor   = errors.New("sync: stale cursor")
	ErrFutureCursor  = errors.New("sync: cursor is ahead of server")
	ErrBadBatch      = errors.New("sync: invalid batch")
	ErrCollection    = errors.New("sync: invalid collection")
	ErrObject        = errors.New("sync: invalid object")
	ErrBlob          = errors.New("sync: blob missing or hash mismatch")
	ErrIdempotency   = errors.New("sync: idempotency key reused with different payload")
)

const (
	OpCreate = "create"
	OpUpdate = "update"
	OpDelete = "delete"
)

type Collection struct {
	ID           string          `json:"id"`
	Kind         string          `json:"kind"`
	Name         string          `json:"name"`
	ParentID     string          `json:"parent_id,omitempty"`
	Revision     uint64          `json:"revision"`
	Metadata     json.RawMessage `json:"metadata"`
	MetadataHash string          `json:"metadata_hash"`
	CreatedAt    int64           `json:"created_at"`
	UpdatedAt    int64           `json:"updated_at"`
	DeletedAt    *int64          `json:"deleted_at,omitempty"`
}

type Object struct {
	ID           string          `json:"id"`
	CollectionID string          `json:"collection_id"`
	Kind         string          `json:"kind"`
	Revision     uint64          `json:"revision"`
	ContentHash  string          `json:"content_hash"`
	MetadataHash string          `json:"metadata_hash"`
	BlobID       string          `json:"blob_id,omitempty"`
	Size         int64           `json:"size"`
	OriginDevice string          `json:"origin_device,omitempty"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    int64           `json:"created_at"`
	UpdatedAt    int64           `json:"updated_at"`
	DeletedAt    *int64          `json:"deleted_at,omitempty"`
}

type Change struct {
	Seq          int64           `json:"seq"`
	ObjectID     string          `json:"object_id"`
	CollectionID string          `json:"collection_id"`
	Op           string          `json:"op"`
	Revision     uint64          `json:"revision"`
	ContentHash  string          `json:"content_hash"`
	MetadataHash string          `json:"metadata_hash"`
	Metadata     json.RawMessage `json:"metadata,omitempty"`
	BlobID       string          `json:"blob_id,omitempty"`
	Size         int64           `json:"size"`
	Deleted      bool            `json:"deleted"`
	Kind         string          `json:"kind,omitempty"`
}

type ChangeInput struct {
	ObjectID     string          `json:"object_id"`
	CollectionID string          `json:"collection_id"`
	Kind         string          `json:"kind"`
	Op           string          `json:"op"`
	BaseRevision uint64          `json:"base_revision"`
	ContentHash  string          `json:"content_hash"`
	MetadataHash string          `json:"metadata_hash"`
	Metadata     json.RawMessage `json:"metadata"`
	BlobID       string          `json:"blob_id"`
	Size         int64           `json:"size"`
	Force        bool            `json:"force"`
}

type PushResult struct {
	ObjectID          string `json:"object_id"`
	Status            string `json:"status"` // ok, conflict, error
	Revision          uint64 `json:"revision,omitempty"`
	Seq               int64  `json:"seq,omitempty"`
	ServerRevision    uint64 `json:"server_revision,omitempty"`
	ServerContentHash string `json:"server_content_hash,omitempty"`
	Error             string `json:"error,omitempty"`
}

type HelloResult struct {
	Protocol      int    `json:"protocol"`
	ServerTimeMS  int64  `json:"server_time_ms"`
	HeadSeq       int64  `json:"head_seq"`
	NeedReconcile bool   `json:"need_reconcile"`
	Reason        string `json:"reason,omitempty"`
}

type ChangesResult struct {
	Changes       []Change `json:"changes"`
	NextSeq       int64    `json:"next_seq"`
	HasMore       bool     `json:"has_more"`
	NeedReconcile bool     `json:"need_reconcile"`
}

type InventoryItem struct {
	ID          string `json:"id"`
	Revision    uint64 `json:"revision"`
	ContentHash string `json:"content_hash"`
}

type ReconcileResult struct {
	MissingOnClient []string   `json:"missing_on_client"`
	MissingOnServer []string   `json:"missing_on_server"`
	Mismatch        []Mismatch `json:"mismatch"`
}

type Mismatch struct {
	ID                string `json:"id"`
	ServerRevision    uint64 `json:"server_revision"`
	ServerContentHash string `json:"server_content_hash"`
}
