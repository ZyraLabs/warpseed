package dispatch

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"warpseed/internal/engine/sftpfast"
	"warpseed/internal/queue"
)

// nopSink swallows events: these tests exercise the pure decision functions,
// which never need a frontend.
type nopSink struct{}

func (nopSink) Emit(string, any) {}

func newTestDispatcher(t *testing.T) (*Dispatcher, *queue.Store) {
	t.Helper()
	store, err := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	// The factory must never be called; a nil one makes that a hard failure
	// rather than a silent dial.
	return New(store, nopSink{}, nil), store
}

func set(t *testing.T, s *queue.Store, key, value string) {
	t.Helper()
	if err := s.SetSetting(key, value); err != nil {
		t.Fatalf("set %s: %v", key, err)
	}
}

const mb = int64(1) << 20

func TestStreamsForUploadUsesUploadSettings(t *testing.T) {
	// Arrange — the two directions are deliberately given different values,
	// so a transfer reading the wrong pair is visible in the count.
	d, s := newTestDispatcher(t)
	set(t, s, "transfers.global_max", "16") // caps are exercised separately
	set(t, s, "transfers.chunk_min_mb", "256")
	set(t, s, "transfers.chunk_streams", "7")
	set(t, s, "transfers.upload_chunk_min_mb", "128")
	set(t, s, "transfers.upload_chunk_streams", "3")

	cases := []struct {
		name      string
		direction string
		size      int64
		want      int
	}{
		{"upload past its own threshold", "upload", 200 * mb, 3},
		{"download past its own threshold", "download", 300 * mb, 7},
		{"upload above download threshold only", "upload", 300 * mb, 3},
		{"download below its threshold but above upload's", "download", 200 * mb, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Act
			got := d.streamsFor(queue.Transfer{
				Engine: "sftpfast", Direction: tc.direction, Size: tc.size,
			}, 16)

			// Assert
			if got != tc.want {
				t.Fatalf("streamsFor = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestStreamsForUploadBelowThresholdIsSingleStream(t *testing.T) {
	// Arrange
	d, s := newTestDispatcher(t)
	set(t, s, "transfers.upload_chunk_min_mb", "128")
	set(t, s, "transfers.upload_chunk_streams", "3")
	up := queue.Transfer{Engine: "sftpfast", Direction: "upload", Size: 127 * mb}

	// Act & Assert
	if got := d.streamsFor(up, 16); got != 1 {
		t.Fatalf("small upload got %d streams, want 1", got)
	}

	// Arrange — 0 disables upload chunking outright, downloads unaffected.
	set(t, s, "transfers.upload_chunk_min_mb", "0")
	big := queue.Transfer{Engine: "sftpfast", Direction: "upload", Size: 8192 * mb}
	down := queue.Transfer{Engine: "sftpfast", Direction: "download", Size: 8192 * mb}

	// Act & Assert
	if got := d.streamsFor(big, 16); got != 1 {
		t.Fatalf("disabled upload chunking got %d streams, want 1", got)
	}
	if got := d.streamsFor(down, 16); got != defaultChunkStream {
		t.Fatalf("download got %d streams, want %d", got, defaultChunkStream)
	}
}

func TestStreamsForUploadRespectsGlobalAndSiteCaps(t *testing.T) {
	// Arrange — an upload can never reserve more connections than the caps.
	d, s := newTestDispatcher(t)
	set(t, s, "transfers.upload_chunk_min_mb", "128")
	set(t, s, "transfers.upload_chunk_streams", "3")
	up := queue.Transfer{Engine: "sftpfast", Direction: "upload", Size: 512 * mb}

	set(t, s, "transfers.global_max", "2")

	// Act & Assert
	if got := d.streamsFor(up, 16); got != 2 {
		t.Fatalf("global cap: got %d streams, want 2", got)
	}

	// Arrange
	set(t, s, "transfers.global_max", "6")

	// Act & Assert
	if got := d.streamsFor(up, 2); got != 2 {
		t.Fatalf("site cap: got %d streams, want 2", got)
	}
	if got := d.streamsFor(up, 1); got != 1 {
		t.Fatalf("site cap of 1: got %d streams, want 1", got)
	}
}

// seedUpload writes a real local source file and queues an upload row for it.
func seedUpload(t *testing.T, s *queue.Store, size int64) (queue.Transfer, string) {
	t.Helper()
	site, err := s.SaveSite(queue.Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	if err != nil {
		t.Fatalf("save site: %v", err)
	}
	src := filepath.Join(t.TempDir(), "payload.bin")
	if err := os.WriteFile(src, make([]byte, size), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	id, err := s.EnqueueTransfer(queue.Transfer{
		SiteID: site, Direction: "upload", Src: src, Dst: "/remote/payload.bin", Size: size,
	})
	if err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	tr, err := s.TransferByID(id)
	if err != nil {
		t.Fatalf("read back transfer: %v", err)
	}
	return tr, src
}

// twoClients is the minimum len(clients) chunking needs. The entries are nil
// on purpose: an upload must never touch a connection to plan, so a remote
// stat sneaking back in panics the test rather than passing quietly.
func twoClients() []*sftpfast.Client { return []*sftpfast.Client{nil, nil} }

func TestChunkPlanUploadStatsLocalNotRemote(t *testing.T) {
	// Arrange
	d, s := newTestDispatcher(t)
	tr, _ := seedUpload(t, s, 4*mb)

	// Act
	ranges, ok := d.chunkPlan(tr, twoClients(), 2)

	// Assert
	if !ok {
		t.Fatal("chunkPlan refused an upload with a valid local source")
	}
	if len(ranges) != 2 {
		t.Fatalf("got %d ranges, want 2", len(ranges))
	}
	var covered int64
	for i, r := range ranges {
		if r.Offset != covered {
			t.Fatalf("range %d starts at %d, want %d", i, r.Offset, covered)
		}
		covered += r.Length
	}
	if covered != tr.Size {
		t.Fatalf("ranges cover %d bytes, want %d", covered, tr.Size)
	}
}

func TestChunkPlanUploadDetectsChangedLocalSource(t *testing.T) {
	// Arrange — plan once so src_mtime is recorded, then bank some progress.
	d, s := newTestDispatcher(t)
	tr, src := seedUpload(t, s, 4*mb)
	if _, ok := d.chunkPlan(tr, twoClients(), 2); !ok {
		t.Fatal("initial chunkPlan refused")
	}
	if err := s.UpdateChunkProgress(tr.ID, 0, 1024, "checkpoint"); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	tr, err := s.TransferByID(tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if tr.SrcMtime == 0 {
		t.Fatal("src mtime was not recorded")
	}

	// Act — the source is rewritten under the plan.
	future := time.Now().Add(2 * time.Hour)
	if err := os.Chtimes(src, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	ranges, ok := d.chunkPlan(tr, twoClients(), 2)

	// Assert — replanned from zero, never resumed onto stale offsets.
	if !ok {
		t.Fatal("chunkPlan refused a same-size source; want a fresh plan")
	}
	for _, r := range ranges {
		if r.Done != 0 {
			t.Fatalf("range %d resumed at %d bytes after the source changed", r.Idx, r.Done)
		}
	}
	back, err := s.TransferByID(tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if back.SrcMtime == tr.SrcMtime {
		t.Fatal("src mtime was not re-recorded against the new source")
	}
}

func TestChunkPlanRejectsNonContiguousPlan(t *testing.T) {
	// Arrange — two ranges that sum to Size but both start at 0. Byte
	// accounting alone would accept this and publish a file with a hole.
	d, s := newTestDispatcher(t)
	tr, _ := seedUpload(t, s, 4*mb)
	if _, ok := d.chunkPlan(tr, twoClients(), 2); !ok {
		t.Fatal("initial chunkPlan refused")
	}
	tr, err := s.TransferByID(tr.ID)
	if err != nil {
		t.Fatal(err)
	}
	half := tr.Size / 2
	if err := s.SaveChunks(tr.ID, []queue.Chunk{
		{TransferID: tr.ID, Idx: 0, Offset: 0, Length: half, State: "pending"},
		{TransferID: tr.ID, Idx: 1, Offset: 0, Length: tr.Size - half, State: "pending"},
	}); err != nil {
		t.Fatalf("save chunks: %v", err)
	}

	// Act
	if _, ok := d.chunkPlan(tr, twoClients(), 2); ok {
		t.Fatal("chunkPlan accepted overlapping ranges")
	}

	// Arrange — a gap is equally fatal, and so is a plan not starting at 0.
	if err := s.SaveChunks(tr.ID, []queue.Chunk{
		{TransferID: tr.ID, Idx: 0, Offset: 0, Length: half - 1, State: "pending"},
		{TransferID: tr.ID, Idx: 1, Offset: half, Length: tr.Size - half + 1, State: "pending"},
	}); err != nil {
		t.Fatalf("save chunks: %v", err)
	}

	// Act & Assert
	if _, ok := d.chunkPlan(tr, twoClients(), 2); ok {
		t.Fatal("chunkPlan accepted a gapped plan")
	}
}
