package protocol

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nubilo/internal/auth"
	ncrypto "nubilo/internal/crypto"
	"nubilo/internal/syncengine"
)

// Client is a signing-device client for /sync/v1.
type Client struct {
	Base     string
	DeviceID string
	Priv     ed25519.PrivateKey
	HTTP     *http.Client
}

func NewClient(base, deviceID string, priv ed25519.PrivateKey, tlsPol TLS) *Client {
	return &Client{
		Base:     strings.TrimRight(base, "/"),
		DeviceID: deviceID,
		Priv:     priv,
		HTTP:     HTTPClient(60*time.Second, tlsPol),
	}
}

func (c *Client) Hello(cursor int64, restoreHint bool) (syncengine.HelloResult, error) {
	var out syncengine.HelloResult
	err := c.doJSON(http.MethodPost, "/sync/v1/hello", map[string]any{
		"protocol_min": 1, "protocol_max": 1, "cursor": cursor, "restore_hint": restoreHint,
	}, &out)
	return out, err
}

func (c *Client) Collections() ([]syncengine.Collection, error) {
	var wrap struct {
		Collections []syncengine.Collection `json:"collections"`
	}
	if err := c.doJSON(http.MethodPost, "/sync/v1/collections", map[string]any{}, &wrap); err != nil {
		return nil, err
	}
	return wrap.Collections, nil
}

func (c *Client) EnsureCollection(kind, name string, metadata json.RawMessage) (*syncengine.Collection, error) {
	return c.EnsureChildCollection(kind, "", name, metadata)
}

func (c *Client) EnsureChildCollection(kind, parentID, name string, metadata json.RawMessage) (*syncengine.Collection, error) {
	var col syncengine.Collection
	err := c.doJSON(http.MethodPost, "/sync/v1/collection", struct {
		Kind     string          `json:"kind"`
		Name     string          `json:"name"`
		ParentID string          `json:"parent_id,omitempty"`
		Metadata json.RawMessage `json:"metadata,omitempty"`
	}{Kind: kind, Name: name, ParentID: parentID, Metadata: metadata}, &col)
	if err != nil {
		return nil, err
	}
	return &col, nil
}

func (c *Client) Changes(since int64, limit int, collectionID string) (syncengine.ChangesResult, error) {
	var out syncengine.ChangesResult
	err := c.doJSON(http.MethodPost, "/sync/v1/changes", map[string]any{
		"since_seq": since, "limit": limit, "collection_id": collectionID,
	}, &out)
	return out, err
}

func (c *Client) Push(idem string, changes []syncengine.ChangeInput) ([]syncengine.PushResult, error) {
	var wrap struct {
		Results []syncengine.PushResult `json:"results"`
	}
	err := c.doJSON(http.MethodPost, "/sync/v1/push", map[string]any{
		"idempotency_key": idem, "changes": changes,
	}, &wrap)
	return wrap.Results, err
}

func (c *Client) Ack(seq int64) error {
	return c.doJSON(http.MethodPost, "/sync/v1/ack", map[string]any{"seq": seq}, nil)
}

func (c *Client) Reconcile(collectionID string, objects []syncengine.InventoryItem) (syncengine.ReconcileResult, error) {
	var out syncengine.ReconcileResult
	err := c.doJSON(http.MethodPost, "/sync/v1/reconcile", map[string]any{
		"collection_id": collectionID, "objects": objects,
	}, &out)
	return out, err
}

func (c *Client) PutBlob(hash string, payload []byte) error {
	path := "/sync/v1/blob/" + strings.ToLower(hash)
	resp, err := c.do(http.MethodPut, path, "application/octet-stream", payload)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("protocol: PUT blob: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (c *Client) GetBlob(hash string) ([]byte, error) {
	path := "/sync/v1/blob/" + strings.ToLower(hash)
	resp, err := c.do(http.MethodGet, path, "", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("protocol: GET blob: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return io.ReadAll(resp.Body)
}

func (c *Client) doJSON(method, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	resp, err := c.do(method, path, "application/json", raw)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("protocol: %s %s: %s %s", method, path, resp.Status, strings.TrimSpace(string(b)))
	}
	if out == nil || len(b) == 0 {
		return nil
	}
	return json.Unmarshal(b, out)
}

func (c *Client) do(method, path, contentType string, body []byte) (*http.Response, error) {
	if body == nil {
		body = []byte{}
	}
	u, err := url.Parse(c.Base + path)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	n, err := ncrypto.Random(16)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", auth.SignRequest(c.Priv, c.DeviceID, method, path, body, time.Now().UnixMilli(), hex.EncodeToString(n)))
	return c.HTTP.Do(req)
}
