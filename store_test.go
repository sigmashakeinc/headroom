package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestStore_RoundTrip verifies that Put followed by Get returns bytes
// byte-identical to the original, including binary/non-UTF8 content.
func TestStore_RoundTrip(t *testing.T) {
	original := []byte("hello world\x00\xff\xfe binary data \x01\x02\x03")
	st := NewStore(t.TempDir())
	h, err := st.Put(original, "text")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if len(h) != 12 {
		t.Errorf("handle should be 12 hex chars, got %q (%d chars)", h, len(h))
	}
	got, err := st.Get(h)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Errorf("round-trip not byte-identical\n  got  %q\n  want %q", got, original)
	}
}

// TestStore_RoundTrip_LargerPayload exercises gzip with a more substantial payload.
func TestStore_RoundTrip_LargerPayload(t *testing.T) {
	payload := bytes.Repeat([]byte("the quick brown fox jumped over the lazy dog\n"), 200)
	st := NewStore(t.TempDir())
	h, err := st.Put(payload, "text")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := st.Get(h)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("large payload round-trip not byte-identical (got %d bytes, want %d)", len(got), len(payload))
	}
}

// TestStore_ContentAddressedDedup verifies identical content maps to the same
// handle and produces only one index entry.
func TestStore_ContentAddressedDedup(t *testing.T) {
	st := NewStore(t.TempDir())
	data := []byte("same content every time")
	h1, err := st.Put(data, "text")
	if err != nil {
		t.Fatalf("Put 1: %v", err)
	}
	h2, err := st.Put(data, "text")
	if err != nil {
		t.Fatalf("Put 2: %v", err)
	}
	if h1 != h2 {
		t.Errorf("dedup: expected same handle, got %q and %q", h1, h2)
	}
	_, _, n := st.Stats()
	if n != 1 {
		t.Errorf("dedup: expected 1 index entry, got %d", n)
	}
}

// TestStore_DifferentContentDifferentHandles verifies distinct content maps to
// distinct handles.
func TestStore_DifferentContentDifferentHandles(t *testing.T) {
	st := NewStore(t.TempDir())
	h1, _ := st.Put([]byte("content alpha"), "text")
	h2, _ := st.Put([]byte("content beta"), "text")
	if h1 == h2 {
		t.Errorf("different content should produce different handles, both got %q", h1)
	}
}

// TestStore_GetUnknownHandle verifies that an unknown handle returns a non-nil error.
func TestStore_GetUnknownHandle(t *testing.T) {
	st := NewStore(t.TempDir())
	_, err := st.Get("notavalidhan")
	if err == nil {
		t.Error("expected error for unknown handle, got nil")
	}
}

// TestStore_Stats_Empty verifies Stats on an empty store returns zeros.
func TestStore_Stats_Empty(t *testing.T) {
	st := NewStore(t.TempDir())
	orig, comp, n := st.Stats()
	if orig != 0 || comp != 0 || n != 0 {
		t.Errorf("empty store stats: orig=%d comp=%d n=%d, want 0,0,0", orig, comp, n)
	}
}

// TestStore_Stats_AfterPut verifies Stats reflects what was Put.
func TestStore_Stats_AfterPut(t *testing.T) {
	st := NewStore(t.TempDir())
	data := []byte("hello stats")
	_, err := st.Put(data, "text")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	origTotal, _, n := st.Stats()
	if n != 1 {
		t.Errorf("expected 1 entry, got %d", n)
	}
	if origTotal != int64(len(data)) {
		t.Errorf("origTotal = %d, want %d", origTotal, len(data))
	}
}

// TestStore_Stats_MultipleEntries sums multiple entries correctly.
func TestStore_Stats_MultipleEntries(t *testing.T) {
	st := NewStore(t.TempDir())
	payloads := [][]byte{
		[]byte("alpha payload"),
		[]byte("beta payload here"),
		[]byte("gamma payload is longer"),
	}
	var wantOrig int64
	for _, p := range payloads {
		st.Put(p, "text")
		wantOrig += int64(len(p))
	}
	origTotal, _, n := st.Stats()
	if n != 3 {
		t.Errorf("expected 3 entries, got %d", n)
	}
	if origTotal != wantOrig {
		t.Errorf("origTotal = %d, want %d", origTotal, wantOrig)
	}
}

// TestStore_PruneOverCap verifies that when the total compressed blob size
// exceeds maxBlobSize, the oldest entry (by Ts) is evicted while the newest survives.
// Ts is controlled by backdating A's index entry to ensure deterministic ordering.
func TestStore_PruneOverCap(t *testing.T) {
	st := NewStore(t.TempDir())

	// Put first entry with default (large) cap so it survives Put.
	dataA := []byte("content alpha - first entry to be pruned eventually")
	hA, err := st.Put(dataA, "text")
	if err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// Read index and backdate A's Ts to Unix epoch so it is definitely older than B.
	entries, err := st.readIndex()
	if err != nil || len(entries) == 0 {
		t.Fatalf("readIndex after Put A: err=%v, entries=%d", err, len(entries))
	}
	compA := entries[0].CompBytes
	entries[0].Ts = 1 // Unix epoch
	if err := st.writeIndex(entries); err != nil {
		t.Fatalf("writeIndex to backdate A: %v", err)
	}

	// Set cap: fits exactly one blob but not two.
	st.maxBlobSize = compA + 1

	// Put second entry (different content = different handle, current Ts > 1).
	dataB := []byte("content beta - second entry, should survive pruning")
	hB, err := st.Put(dataB, "text")
	if err != nil {
		t.Fatalf("Put B: %v", err)
	}

	// A should be evicted (it was oldest).
	_, err = st.Get(hA)
	if err == nil {
		t.Error("expected Get(hA) to fail after prune, but it succeeded")
	}

	// B should survive (it was newest).
	got, err := st.Get(hB)
	if err != nil {
		t.Errorf("Get(hB) failed: %v", err)
	}
	if !bytes.Equal(got, dataB) {
		t.Error("Get(hB) is not byte-identical to dataB after prune")
	}

	// Stats should reflect only 1 remaining entry.
	_, _, n := st.Stats()
	if n != 1 {
		t.Errorf("expected 1 entry after prune, got %d", n)
	}
}

// TestStore_PruneTTL verifies that entries with Ts older than ttlSecs are evicted.
// A fake old index entry (Ts=1) is injected directly so no time.Sleep is needed.
func TestStore_PruneTTL(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	// Put a real entry that should survive (current Ts, within default 24h TTL).
	dataA := []byte("still alive content - real entry")
	hA, err := st.Put(dataA, "text")
	if err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// Inject a fake old entry into the index (Ts=1 = Unix epoch, definitely expired).
	fakeEntry := indexEntry{
		Handle:    "000000000001",
		Kind:      "text",
		OrigBytes: 10,
		CompBytes: 5,
		Ts:        1, // Unix epoch — expired under any positive TTL
	}
	if err := st.appendIndex(fakeEntry); err != nil {
		t.Fatalf("appendIndex fake: %v", err)
	}

	// Prune should evict the fake old entry. os.Remove of the missing blob is silently ignored.
	if err := st.prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// Fake entry should be gone from the index.
	entries, err := st.readIndex()
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}
	for _, e := range entries {
		if e.Handle == fakeEntry.Handle {
			t.Errorf("expired fake entry still in index after prune: %+v", e)
		}
	}

	// Real entry A should still be retrievable.
	got, err := st.Get(hA)
	if err != nil {
		t.Errorf("Get(hA) after TTL prune: %v", err)
	}
	if !bytes.Equal(got, dataA) {
		t.Error("real entry corrupted by TTL prune")
	}
}

// TestStore_DefaultStoreRoot_EnvOverride verifies HEADROOM_HOME is honored.
func TestStore_DefaultStoreRoot_EnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEADROOM_HOME", dir)
	got := defaultStoreRoot()
	if got != dir {
		t.Errorf("HEADROOM_HOME=%q: defaultStoreRoot()=%q", dir, got)
	}
}

// TestStore_DefaultStoreRoot_Fallback verifies the fallback path contains
// the expected store dir components.
func TestStore_DefaultStoreRoot_Fallback(t *testing.T) {
	t.Setenv("HEADROOM_HOME", "")
	got := defaultStoreRoot()
	if got == "" {
		t.Fatal("defaultStoreRoot fallback returned empty string")
	}
	if !strings.Contains(got, ".sigmashake") {
		t.Errorf("defaultStoreRoot fallback %q does not contain .sigmashake", got)
	}
}

// TestStore_ReadIndex_MissingFile verifies readIndex returns nil,nil for a
// store with no index yet (non-existent root or index file).
func TestStore_ReadIndex_MissingFile(t *testing.T) {
	st := NewStore(t.TempDir())
	entries, err := st.readIndex()
	if err != nil {
		t.Errorf("readIndex on missing file should return nil error, got %v", err)
	}
	if entries != nil {
		t.Errorf("readIndex on missing file should return nil entries, got %v", entries)
	}
}

// TestStore_HandleLength verifies sha256handle always returns exactly 12 lower-hex chars.
func TestStore_HandleLength(t *testing.T) {
	cases := [][]byte{
		{},
		[]byte("hello"),
		[]byte("a much longer piece of content to hash"),
		make([]byte, 1024),
	}
	for _, c := range cases {
		h := sha256handle(c)
		if len(h) != 12 {
			t.Errorf("sha256handle: got %q (len %d), want 12 chars", h, len(h))
		}
		for _, ch := range h {
			if !((ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f')) {
				t.Errorf("sha256handle: non-lower-hex char %q in %q", ch, h)
			}
		}
	}
}

// TestStore_IndexPersistence verifies the index and blobs survive across Store
// instances pointing at the same root.
func TestStore_IndexPersistence(t *testing.T) {
	dir := t.TempDir()
	data := []byte("persistent content")

	st1 := NewStore(dir)
	h, err := st1.Put(data, "text")
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	st2 := NewStore(dir)
	got, err := st2.Get(h)
	if err != nil {
		t.Fatalf("Get from second Store: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Error("persistence: round-trip not byte-identical across Store instances")
	}

	_, _, n := st2.Stats()
	if n != 1 {
		t.Errorf("persistence: expected 1 entry in second Store, got %d", n)
	}
}

// TestStore_Get_CorruptBlob verifies Get returns an error when the blob file
// is not a valid gzip stream (covers the gzip.NewReader error branch).
func TestStore_Get_CorruptBlob(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handle := "corrupthandle"
	blobPath := st.root + "/" + handle + blobExt
	if err := os.WriteFile(blobPath, []byte("this is not a gzip stream"), 0o600); err != nil {
		t.Fatalf("WriteFile corrupt blob: %v", err)
	}
	_, err := st.Get(handle)
	if err == nil {
		t.Error("expected error for corrupt (non-gzip) blob, got nil")
	}
}

// TestStore_Put_WriteError verifies Put propagates an error when the blob
// cannot be created (read-only store root → writeGzipBlob fails).
func TestStore_Put_WriteError(t *testing.T) {
	if !permsEnforced() {
		t.Skip("chmod restrictions do not apply to root")
	}
	root := t.TempDir()
	st := NewStore(root)
	// Create root dir first (Put calls MkdirAll, which succeeds on existing dir).
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Make root read-only: MkdirAll passes (dir exists), but os.Create fails.
	if err := os.Chmod(root, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(root, 0o700)

	_, err := st.Put([]byte("blob write will fail"), "text")
	if err == nil {
		t.Error("expected error with read-only store root, got nil")
	}
}

// TestStore_ReadIndex_PermissionError verifies readIndex returns a non-nil
// error when the index file exists but cannot be read (covers the non-
// ErrNotExist open-error branch).
func TestStore_ReadIndex_PermissionError(t *testing.T) {
	if !permsEnforced() {
		t.Skip("chmod restrictions do not apply to root")
	}
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Create the index file, then remove read permission.
	f, err := os.Create(st.indexPath())
	if err != nil {
		t.Fatalf("Create index: %v", err)
	}
	f.Close()
	if err := os.Chmod(st.indexPath(), 0o000); err != nil {
		t.Fatalf("Chmod index: %v", err)
	}
	defer os.Chmod(st.indexPath(), 0o600)

	_, err = st.readIndex()
	if err == nil {
		t.Error("expected error reading 0o000-permission index file, got nil")
	}
}

// TestStore_AppendIndex_WriteError verifies appendIndex returns an error when
// the index file cannot be opened (covers the os.OpenFile error branch).
func TestStore_AppendIndex_WriteError(t *testing.T) {
	if !permsEnforced() {
		t.Skip("chmod restrictions do not apply to root")
	}
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Create the index file and make it write-protected.
	f, err := os.Create(st.indexPath())
	if err != nil {
		t.Fatalf("Create index: %v", err)
	}
	f.Close()
	if err := os.Chmod(st.indexPath(), 0o444); err != nil {
		t.Fatalf("Chmod index: %v", err)
	}
	defer os.Chmod(st.indexPath(), 0o600)

	err = st.appendIndex(indexEntry{Handle: "aabbccddeeff", Kind: "text", Ts: 1})
	if err == nil {
		t.Error("expected error appending to read-only index file, got nil")
	}
}

// TestStore_Put_AppendIndexError verifies Put propagates an error when
// appendIndex fails (covers the appendIndex-error return in Put).
// Trigger: make the index path a directory so OpenFile fails with EISDIR.
func TestStore_Put_AppendIndexError(t *testing.T) {
	st := NewStore(t.TempDir())
	// Make the index path a directory — appendIndex tries to open it O_WRONLY
	// and gets EISDIR.
	if err := os.MkdirAll(st.indexPath(), 0o700); err != nil {
		t.Fatalf("MkdirAll index-as-dir: %v", err)
	}
	_, err := st.Put([]byte("data for appendindex error path"), "text")
	if err == nil {
		t.Error("expected error when index path is a directory, got nil")
	}
}

// TestStore_WriteIndex_CreateTempError verifies writeIndex returns an error
// when os.CreateTemp fails (non-existent root directory).
func TestStore_WriteIndex_CreateTempError(t *testing.T) {
	// Use a guaranteed non-existent subdir inside a fresh TempDir.
	st := NewStore(filepath.Join(t.TempDir(), "nonexistent-subdir"))
	entries := []indexEntry{
		{Handle: "aabbccddeeff", Kind: "text", OrigBytes: 10, CompBytes: 4, Ts: 1},
	}
	err := st.writeIndex(entries)
	if err == nil {
		t.Error("expected error from writeIndex with non-existent root, got nil")
	}
}

// TestWriteGzipBlob_CreateError verifies writeGzipBlob returns an error when
// os.Create fails (parent directory does not exist).
func TestWriteGzipBlob_CreateError(t *testing.T) {
	// Target a file inside a guaranteed non-existent subdirectory.
	badPath := filepath.Join(t.TempDir(), "nonexistent-subdir", "test.gz")
	_, err := writeGzipBlob(badPath, []byte("data"))
	if err == nil {
		t.Error("expected error from writeGzipBlob with non-existent parent dir, got nil")
	}
}

// TestStore_Get_UnreadableBlob verifies Get returns a non-ErrNotExist error
// when the blob file exists but cannot be opened (covers the open-error branch
// that is not ErrNotExist).
func TestStore_Get_UnreadableBlob(t *testing.T) {
	if !permsEnforced() {
		t.Skip("chmod restrictions do not apply to root")
	}
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handle := "unreadblob01"
	blobPath := st.root + "/" + handle + blobExt
	if err := os.WriteFile(blobPath, []byte("placeholder"), 0o000); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	defer os.Chmod(blobPath, 0o600)

	_, err := st.Get(handle)
	if err == nil {
		t.Error("expected error for unreadable blob, got nil")
	}
}

// TestStore_Put_MkdirAllError verifies Put propagates an error when
// os.MkdirAll cannot create the store root (a regular file occupies a
// path component, causing ENOTDIR). Covers block 64.51,66.3.
func TestStore_Put_MkdirAllError(t *testing.T) {
	dir := t.TempDir()
	// Place a regular file where a directory component must exist.
	blockingFile := filepath.Join(dir, "notadir")
	if err := os.WriteFile(blockingFile, []byte{}, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	// Root tries to go through blockingFile as if it were a directory.
	st := NewStore(filepath.Join(blockingFile, "headroom"))
	_, err := st.Put([]byte("mkdirall will fail"), "text")
	if err == nil {
		t.Error("expected error when MkdirAll cannot create store root, got nil")
	}
}

// TestStore_Get_TruncatedGzip verifies Get returns a non-nil error when the
// gzip header is valid but the stream body is absent (io.ReadAll fails).
// Covers block 114.16,116.3.
func TestStore_Get_TruncatedGzip(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	handle := "truncatedgzip1"
	blobPath := filepath.Join(st.root, handle+blobExt)
	// A minimal valid 10-byte gzip header with no compressed body.
	// gzip.NewReader parses the header successfully; io.ReadAll then
	// returns io.ErrUnexpectedEOF when the DEFLATE body is missing.
	gzipHeader := []byte{0x1f, 0x8b, 0x08, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xff}
	if err := os.WriteFile(blobPath, gzipHeader, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	_, err := st.Get(handle)
	if err == nil {
		t.Error("expected error reading truncated gzip blob, got nil")
	}
}

// TestStore_Prune_EmptyStoreEarlyReturn verifies prune returns nil immediately
// when the index is empty (len(entries)==0 early-return path). Covers 135.37,137.3.
func TestStore_Prune_EmptyStoreEarlyReturn(t *testing.T) {
	// No index file — readIndex returns nil,nil; len(entries)==0 → return nil.
	st := NewStore(t.TempDir())
	if err := st.prune(); err != nil {
		t.Errorf("prune on empty store should return nil, got %v", err)
	}
}

// TestStore_Prune_CapEvictionLoopBody verifies the cap-eviction body inside
// prune's for loop (keep[i]=false; totalComp -= ...). Covers block 166.3,167.36.
// A is fresh (not TTL-expired) but total compressed size exceeds cap, so cap
// eviction must mark A for removal.
func TestStore_Prune_CapEvictionLoopBody(t *testing.T) {
	st := NewStore(t.TempDir())

	dataA := []byte("cap eviction target — something to compress and store")
	hA, err := st.Put(dataA, "text")
	if err != nil {
		t.Fatalf("Put A: %v", err)
	}

	// Read compA from the index.
	entries, err := st.readIndex()
	if err != nil || len(entries) == 0 {
		t.Fatalf("readIndex after Put A: err=%v len=%d", err, len(entries))
	}
	compA := entries[0].CompBytes

	// Set cap just below A's size — A is fresh (TTL ok) but exceeds cap.
	st.maxBlobSize = compA - 1

	// Prune directly: TTL keeps A, but totalComp(compA) > maxBlobSize(compA-1)
	// → cap loop sets keep[0]=false and totalComp -= compA.
	if err := st.prune(); err != nil {
		t.Fatalf("prune: %v", err)
	}

	// A must be evicted by cap (blob removed).
	if _, err := st.Get(hA); err == nil {
		t.Error("expected Get(hA) to fail after cap eviction, but it succeeded")
	}
}

// TestStore_ReadIndex_SkipsBlankLines verifies readIndex skips blank lines in
// the index file without returning an error. Covers block 206.21,207.12.
func TestStore_ReadIndex_SkipsBlankLines(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	// Write one valid JSON entry followed by a blank line.
	content := `{"handle":"aabbccddeeff","kind":"text","orig_bytes":10,"comp_bytes":5,"ts":1000}` + "\n\n"
	if err := os.WriteFile(st.indexPath(), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	entries, err := st.readIndex()
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}
	if len(entries) != 1 {
		t.Errorf("expected 1 entry (blank line skipped), got %d", len(entries))
	}
}

// TestStore_WriteIndex_AtomicReplacement verifies writeIndex replaces the
// index file and readIndex reads it back correctly.
func TestStore_WriteIndex_AtomicReplacement(t *testing.T) {
	st := NewStore(t.TempDir())
	if err := os.MkdirAll(st.root, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	entries := []indexEntry{
		{Handle: "aabbccddeeff", Kind: "text", OrigBytes: 100, CompBytes: 40, Ts: 1000},
	}
	if err := st.writeIndex(entries); err != nil {
		t.Fatalf("writeIndex: %v", err)
	}
	got, err := st.readIndex()
	if err != nil {
		t.Fatalf("readIndex: %v", err)
	}
	if len(got) != 1 || got[0].Handle != "aabbccddeeff" || got[0].OrigBytes != 100 {
		t.Errorf("writeIndex round-trip: got %+v", got)
	}
}
