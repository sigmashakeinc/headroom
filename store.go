package main

import (
	"bufio"
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const (
	storeDirName   = "headroom"
	indexFileName  = "index.jsonl"
	blobExt        = ".gz"
	defaultMaxBlob = 64 * 1024 * 1024 // 64 MiB total compressed blob bytes
	defaultTTLSecs = 86400             // 24 hours
)

// Store is the reversible cache: each Put gzip-compresses the original and
// writes a content-addressed blob; Get gunzips it back, byte-identical.
type Store struct {
	root        string
	maxBlobSize int64 // prune threshold: total compressed blob bytes on disk
	ttlSecs     int64 // prune threshold: age of entry in seconds
}

// indexEntry is one record in index.jsonl.
type indexEntry struct {
	Handle    string `json:"handle"`
	Kind      string `json:"kind"`
	OrigBytes int64  `json:"orig_bytes"`
	CompBytes int64  `json:"comp_bytes"`
	Ts        int64  `json:"ts"` // unix seconds
}

// NewStore creates a Store rooted at root.
func NewStore(root string) *Store {
	return &Store{root: root, maxBlobSize: defaultMaxBlob, ttlSecs: defaultTTLSecs}
}

// defaultStoreRoot returns the store root, honoring HEADROOM_HOME env.
func defaultStoreRoot() string {
	if h := os.Getenv("HEADROOM_HOME"); h != "" {
		return h
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".sigmashake", storeDirName)
	}
	return filepath.Join(home, ".sigmashake", storeDirName)
}

// Put stores original bytes gzip-compressed as a blob, appends an index
// entry, and prunes if needed. Returns the handle (sha256 prefix, 12 hex
// chars). Content-addressed: identical content returns the same handle
// without re-writing.
func (s *Store) Put(original []byte, kind string) (string, error) {
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return "", fmt.Errorf("headroom store mkdir: %w", err)
	}
	h := sha256handle(original)
	blobPath := filepath.Join(s.root, h+blobExt)

	// Dedup: blob already present — skip write, return existing handle.
	if _, err := os.Stat(blobPath); err == nil {
		return h, nil
	}

	// Gzip-compress the original to the blob file.
	compBytes, err := writeGzipBlob(blobPath, original)
	if err != nil {
		return "", fmt.Errorf("headroom store write blob: %w", err)
	}

	entry := indexEntry{
		Handle:    h,
		Kind:      kind,
		OrigBytes: int64(len(original)),
		CompBytes: compBytes,
		Ts:        time.Now().Unix(),
	}
	if err := s.appendIndex(entry); err != nil {
		return "", fmt.Errorf("headroom store append index: %w", err)
	}

	_ = s.prune()
	return h, nil
}

// Get retrieves the original bytes for handle, byte-identical to what was
// Put. Returns an error for unknown handles.
func (s *Store) Get(handle string) ([]byte, error) {
	blobPath := filepath.Join(s.root, handle+blobExt)
	f, err := os.Open(blobPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("headroom: unknown handle %q", handle)
		}
		return nil, fmt.Errorf("headroom get open: %w", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("headroom get gzip reader: %w", err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		return nil, fmt.Errorf("headroom get read: %w", err)
	}
	return data, nil
}

// Stats returns aggregate totals from the index.
func (s *Store) Stats() (origTotal, compTotal int64, n int) {
	entries, _ := s.readIndex()
	for _, e := range entries {
		origTotal += e.OrigBytes
		compTotal += e.CompBytes
		n++
	}
	return
}

// prune evicts entries that are TTL-expired and/or push total blob size over
// maxBlobSize, evicting oldest first. Blob files are deleted; index is rewritten.
func (s *Store) prune() error {
	entries, err := s.readIndex()
	if err != nil || len(entries) == 0 {
		return err
	}

	// Sort ascending by Ts (oldest first) for LRU eviction.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Ts < entries[j].Ts })

	now := time.Now().Unix()
	cutoff := now - s.ttlSecs

	keep := make([]bool, len(entries))
	for i, e := range entries {
		keep[i] = e.Ts >= cutoff // false = expired
	}

	// Compute total compressed size for entries still kept after TTL eviction.
	var totalComp int64
	for i, e := range entries {
		if keep[i] {
			totalComp += e.CompBytes
		}
	}

	// Evict oldest non-expired entries while over cap.
	for i := range entries {
		if !keep[i] {
			continue
		}
		if totalComp <= s.maxBlobSize {
			break
		}
		keep[i] = false
		totalComp -= entries[i].CompBytes
	}

	// Delete blobs for evicted entries and rewrite index.
	changed := false
	kept := make([]indexEntry, 0, len(entries))
	for i, e := range entries {
		if !keep[i] {
			_ = os.Remove(filepath.Join(s.root, e.Handle+blobExt))
			changed = true
		} else {
			kept = append(kept, e)
		}
	}
	if changed {
		return s.writeIndex(kept)
	}
	return nil
}

// --- index helpers ---

func (s *Store) indexPath() string {
	return filepath.Join(s.root, indexFileName)
}

func (s *Store) readIndex() ([]indexEntry, error) {
	f, err := os.Open(s.indexPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var entries []indexEntry
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e indexEntry
		if err := json.Unmarshal(line, &e); err == nil {
			entries = append(entries, e)
		}
	}
	return entries, sc.Err()
}

func (s *Store) appendIndex(e indexEntry) error {
	f, err := os.OpenFile(s.indexPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	// indexEntry contains only basic types; json.Marshal cannot error here.
	b, _ := json.Marshal(e)
	_, err = f.Write(append(b, '\n'))
	return err
}

func (s *Store) writeIndex(entries []indexEntry) error {
	tmpFile, err := os.CreateTemp(s.root, "index-*.jsonl.tmp")
	if err != nil {
		return err
	}
	tmpName := tmpFile.Name()
	w := bufio.NewWriter(tmpFile)
	for _, e := range entries {
		b, err := json.Marshal(e)
		if err != nil {
			tmpFile.Close()
			os.Remove(tmpName)
			return err
		}
		w.Write(b)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmpFile.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, s.indexPath())
}

// --- blob helpers ---

// writeGzipBlob gzip-compresses data into path and returns the compressed size.
// Written 0o600 (not os.Create's default 0o666-minus-umask): cached blobs can
// hold the byte-identical original of any compressed command output, so the
// file itself — not just the parent directory's 0o700 — stays owner-only.
func writeGzipBlob(path string, data []byte) (compBytes int64, err error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()
	gw := gzip.NewWriter(f)
	if _, err = gw.Write(data); err != nil {
		return 0, err
	}
	if err = gw.Close(); err != nil {
		return 0, err
	}
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	return fi.Size(), nil
}

// sha256handle returns the first 12 hex characters of sha256(data).
func sha256handle(data []byte) string {
	sum := sha256.Sum256(data)
	return fmt.Sprintf("%x", sum)[:12]
}
