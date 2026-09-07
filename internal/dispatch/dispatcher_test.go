package dispatch

import (
	"os"
	"path/filepath"
	"strconv"
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

// TestAdmissionWaitsForFullLaneWidth pins the rule the 1.1.3 fix exists for:
// a queue of large files must run one file at its configured lane count, not
// every file on a single leftover connection.
func TestAdmissionWaitsForFullLaneWidth(t *testing.T) {
	// Arrange — a 4-lane download already running against a budget of 6.
	d, _ := newTestDispatcher(t)
	const siteID = int64(1)
	d.activeN = 4
	d.perSite[siteID] = 4

	// Act & Assert — the next 4-lane file does not fit in the 2 spare
	// connections, and must wait rather than start narrow.
	if d.fits(siteID, 4, 6, 6) {
		t.Fatal("4 lanes admitted with only 2 connections free")
	}
	// A single-lane transfer still uses the spare capacity.
	if !d.fits(siteID, 1, 6, 6) {
		t.Fatal("1 lane refused with 2 connections free")
	}
	// Once the running transfer drains, the full width fits.
	d.activeN, d.perSite[siteID] = 0, 0
	if !d.fits(siteID, 4, 6, 6) {
		t.Fatal("4 lanes refused on an idle site")
	}
}

// TestAdmissionAlwaysFitsAnIdleSite is the no-starvation guarantee that
// makes wait-for-full-width safe: streamsFor clamps to both caps, so
// whatever it returns must be admissible once everything else drains.
func TestAdmissionAlwaysFitsAnIdleSite(t *testing.T) {
	// Arrange
	d, s := newTestDispatcher(t)
	set(t, s, "transfers.chunk_min_mb", "256")
	set(t, s, "transfers.chunk_streams", "16")
	big := queue.Transfer{Engine: "sftpfast", Direction: "download", Size: 4096 * mb}

	for _, caps := range []struct{ global, site int }{{1, 1}, {2, 1}, {6, 3}, {8, 8}, {6, 16}} {
		// Act
		set(t, s, "transfers.global_max", itoa(caps.global))
		streams := d.streamsFor(big, caps.site)

		// Assert
		if !d.fits(2, streams, caps.global, caps.site) {
			t.Fatalf("caps %d/%d: streamsFor asked for %d, which never fits",
				caps.global, caps.site, streams)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// TestGrantedCeilingKeepsTheQueueMoving is the counterweight to
// wait-for-full-width: when the SERVER is the thing refusing connections,
// holding out for the configured width would idle most of the budget for
// the length of a 50 GB transfer.
func TestGrantedCeilingKeepsTheQueueMoving(t *testing.T) {
	// Arrange — T1 admitted at 3 of a site budget of 3, global 6.
	d, _ := newTestDispatcher(t)
	const siteID = int64(1)
	const globalCap, siteCap = 6, 3
	t1 := queue.Transfer{ID: 1, SiteID: siteID}
	d.slots[t1.ID], d.perSite[siteID], d.activeN = 3, 3, 3

	// Act — the server grants only one connection of the three.
	d.resize(t1, 1)

	// Assert — the two slots come back AND the ceiling is remembered.
	if d.perSite[siteID] != 1 || d.activeN != 1 {
		t.Fatalf("after resize perSite=%d activeN=%d, want 1 and 1", d.perSite[siteID], d.activeN)
	}
	if got := d.granted[siteID]; got.n != 1 {
		t.Fatalf("granted ceiling = %d, want 1", got.n)
	}

	// Act — the next transfer asks for the configured 3.
	streams := d.clampToGranted(siteID, 3)

	// Assert — it asks close to what the server actually gives, and runs now
	// instead of waiting hours for a width that will never be free. It stops
	// at 2, never 1: a single lane would send a part-transferred file down
	// the linear path, which destroys its chunk plan.
	if streams != 2 {
		t.Fatalf("clamped request = %d, want 2 (never 1 for a chunked file)", streams)
	}
	if !d.fits(siteID, streams, globalCap, siteCap) {
		t.Fatal("clamped request still does not fit: the queue would stall")
	}

	// Act — the site drains.
	d.slots[t1.ID] = 1
	d.release(t1)

	// Assert — the ceiling is forgotten, so a limit that has lifted is
	// re-probed rather than believed for the rest of the session.
	if _, ok := d.granted[siteID]; ok {
		t.Fatal("granted ceiling survived the site going idle")
	}
	if got := d.clampToGranted(siteID, 3); got != 3 {
		t.Fatalf("request after idle = %d, want the configured 3", got)
	}
}

// TestGrantedCeilingExpires stops one unlucky dial from holding a site at a
// single lane for a whole overnight run: a busy site never goes idle, so the
// idle reset alone would never fire.
func TestGrantedCeilingExpires(t *testing.T) {
	// Arrange — a ceiling observed longer ago than the TTL, on a site that
	// has stayed busy throughout.
	d, _ := newTestDispatcher(t)
	const siteID = int64(1)
	d.perSite[siteID], d.activeN = 1, 1
	d.granted[siteID] = grant{n: 1, at: time.Now().Add(-grantTTL - time.Second)}

	// Act
	got := d.clampToGranted(siteID, 4)

	// Assert — the stale observation is dropped, not believed.
	if got != 4 {
		t.Fatalf("stale ceiling still clamped request to %d, want 4", got)
	}
	if _, ok := d.granted[siteID]; ok {
		t.Fatal("stale ceiling was left in the map")
	}

	// Arrange — a fresh observation is still honoured.
	d.granted[siteID] = grant{n: 2, at: time.Now()}

	// Act & Assert
	if got := d.clampToGranted(siteID, 4); got != 2 {
		t.Fatalf("fresh ceiling gave %d, want 2", got)
	}

	// Arrange — a server granting one connection.
	d.granted[siteID] = grant{n: 1, at: time.Now()}

	// Act & Assert — a single-lane transfer is left alone, but a chunked one
	// is floored at 2 so the shortfall is caught and requeued rather than
	// silently discarding its plan.
	if got := d.clampToGranted(siteID, 1); got != 1 {
		t.Fatalf("single-lane request became %d, want 1", got)
	}
	if got := d.clampToGranted(siteID, 8); got != 2 {
		t.Fatalf("chunked request clamped to %d, want a floor of 2", got)
	}
}

// TestDstKeySeparatesSitesAndDirections — the per-destination lock is only
// as good as its key. An upload's destination is remote and unique only
// within its site; a download's is a local path.
func TestDstKeySeparatesSitesAndDirections(t *testing.T) {
	up1 := queue.Transfer{SiteID: 1, Direction: "upload", Dst: "/seed/a.mkv"}
	up2 := queue.Transfer{SiteID: 2, Direction: "upload", Dst: "/seed/a.mkv"}
	down := queue.Transfer{SiteID: 1, Direction: "download", Dst: "/seed/a.mkv"}

	if dstKey(up1) == dstKey(up2) {
		t.Fatal("the same remote path on two different servers shares a key")
	}
	if dstKey(up1) == dstKey(down) {
		t.Fatal("a remote destination collides with a local one")
	}
	if dstKey(up1) != dstKey(queue.Transfer{SiteID: 1, Direction: "upload", Dst: "/seed/a.mkv"}) {
		t.Fatal("the same destination does not produce a stable key")
	}
	// Downloads from different sites to one local path MUST collide: they
	// write the same file.
	if dstKey(down) != dstKey(queue.Transfer{SiteID: 9, Direction: "download", Dst: "/seed/a.mkv"}) {
		t.Fatal("two downloads onto one local path were treated as separate files")
	}
}
