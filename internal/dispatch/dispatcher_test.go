package dispatch

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
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

// TestLaneGroupSeparatesDirections — head-of-line blocking is a promise about
// queue ORDER, which only holds within one direction. An upload that cannot
// fit has no business stopping downloads from being considered; keyed by site
// alone, one upload waiting for width froze every download to that server.
func TestLaneGroupSeparatesDirections(t *testing.T) {
	up := queue.Transfer{SiteID: 1, Direction: "upload"}
	down := queue.Transfer{SiteID: 1, Direction: "download"}
	other := queue.Transfer{SiteID: 2, Direction: "upload"}

	if laneGroup(up) == laneGroup(down) {
		t.Fatal("an upload and a download on one site share a blocking group")
	}
	if laneGroup(up) == laneGroup(other) {
		t.Fatal("the same direction on two sites shares a blocking group")
	}
	if laneGroup(up) != laneGroup(queue.Transfer{SiteID: 1, Direction: "upload"}) {
		t.Fatal("the same site and direction does not produce a stable group")
	}
}

// TestUploadDoesNotConsumeTheWholeSiteBudget documents the arithmetic the
// shipped defaults actually produce, so a change to either default has to
// face this test rather than quietly re-creating the problem.
func TestUploadDoesNotConsumeTheWholeSiteBudget(t *testing.T) {
	// Arrange — the shipped defaults.
	d, s := newTestDispatcher(t)
	set(t, s, "transfers.global_max", itoa(defaultGlobalCap))
	set(t, s, "transfers.chunk_min_mb", "256")
	set(t, s, "transfers.chunk_streams", itoa(defaultChunkStream))
	set(t, s, "transfers.upload_chunk_min_mb", "128")
	set(t, s, "transfers.upload_chunk_streams", itoa(defaultUploadChunkStream))

	big := int64(4096) << 20
	up := queue.Transfer{Engine: "sftpfast", Direction: "upload", Size: big}
	down := queue.Transfer{Engine: "sftpfast", Direction: "download", Size: big}

	// The budget a real install has. Migration 013 moves existing databases
	// onto it; defaultSiteCap is what a fresh one starts with.
	const shippedSiteCap = defaultSiteCap
	upLanes := d.streamsFor(up, shippedSiteCap)
	downLanes := d.streamsFor(down, shippedSiteCap)

	// Assert — one upload and one download must fit together. They currently
	// do not: 3 + 4 = 7 against a budget of 6, so whichever direction claims
	// the connections first holds them for the whole length of a 50 GB
	// transfer while the other waits. This test is the invariant; the fix is
	// either a bigger budget or a smaller shipped lane count, and whichever
	// is chosen has to face this assertion.
	if upLanes+downLanes > shippedSiteCap {
		t.Fatalf("an upload (%d lanes) and a download (%d lanes) need %d connections "+
			"but the shipped per-site budget is %d — one direction starves the other",
			upLanes, downLanes, upLanes+downLanes, shippedSiteCap)
	}
}

// recSink records every event so a test can assert what the frontend would
// have been told.
type recSink struct {
	mu     sync.Mutex
	events []string
}

func (r *recSink) Emit(name string, _ any) {
	r.mu.Lock()
	r.events = append(r.events, name)
	r.mu.Unlock()
}

func (r *recSink) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e == name {
			n++
		}
	}
	return n
}

func TestPausedQueueClaimsNothing(t *testing.T) {
	// Arrange — a pending row, plenty of budget, and the persisted pause
	// flag set as a previous run would have left it.
	store, err := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	site, err := store.SaveSite(queue.Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: filepath.Join(t.TempDir(), "a"), Size: 10})
	set(t, store, "queue.paused", "1")
	// A nil factory: any claim would dial and panic, which is the failure
	// this test exists to catch.
	d := New(store, nopSink{}, nil)

	// Act
	if !d.Paused() {
		t.Fatal("dispatcher did not restore the persisted pause")
	}
	d.pump(context.Background())

	// Assert
	if got, _ := store.TransferByID(id); got.State != "pending" {
		t.Fatalf("row is %q after a pump on a paused queue, want pending", got.State)
	}
}

func TestSetPausedPersistsAndRequeuesRunningTransfers(t *testing.T) {
	// Arrange — a running transfer whose engine blocks until its context is
	// cancelled, standing in for a copy mid-flight.
	store, err := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	site, _ := store.SaveSite(queue.Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: "/l/a", Size: 10})
	// One real retry, so the row carries a spent attempt the reset must
	// wind back.
	_ = store.SetTransferState(id, "active", nil)
	_, _ = store.ScheduleRetryIfActive(id, "2000-01-01T00:00:00Z", nil)
	sink := &recSink{}
	d := New(store, sink, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tctx, tcancel := context.WithCancel(ctx)
	d.mu.Lock()
	d.cancels[id] = tcancel
	d.slots[id] = 1
	d.activeN = 1
	d.mu.Unlock()
	if won, _ := store.ClaimPending(id); !won {
		t.Fatal("claim")
	}
	finished := make(chan struct{})
	d.running.Add(1)
	go func() {
		defer d.running.Done()
		defer d.release(queue.Transfer{ID: id, SiteID: site})
		<-tctx.Done()
		d.finishWithError(tctx, queue.Transfer{ID: id, SiteID: site, Src: "/a", Attempt: 1}, context.Canceled)
		close(finished)
	}()

	// Act
	if err := d.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("running transfer did not stop after SetPaused")
	}

	// Assert — persisted, flagged, and the row is a clean pending, not a
	// failed or backed-off one.
	if store.Setting("queue.paused", "") != "1" || !d.Paused() {
		t.Fatalf("pause not persisted: setting=%q flag=%v", store.Setting("queue.paused", ""), d.Paused())
	}
	got, _ := store.TransferByID(id)
	if got.State != "pending" || got.Attempt != 0 || got.NextRetryAt != nil {
		t.Fatalf("row after pause = %q attempt %d retryAt %v; want a clean pending", got.State, got.Attempt, got.NextRetryAt)
	}
	if sink.count("queue:paused") != 1 {
		t.Errorf("queue:paused emitted %d times, want 1", sink.count("queue:paused"))
	}

	// Act — resume nudges the pump and persists the flip.
	if err := d.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	if d.Paused() || store.Setting("queue.paused", "") != "0" {
		t.Fatal("resume did not clear the persisted pause")
	}
	select {
	case <-d.wake:
	default:
		t.Error("resume did not wake the pump")
	}
}

func TestCancelManyDiscardsPlaceholdersAndEmitsOnce(t *testing.T) {
	// Arrange — three queued downloads; one paused with a real .wspart on
	// disk, one that never wrote a byte, one already completed (must survive).
	store, err := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	site, _ := store.SaveSite(queue.Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	dir := t.TempDir()
	partDst := filepath.Join(dir, "part.bin")
	part := partDst + sftpfast.PartSuffix
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	paused, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/p", Dst: partDst, Size: 8})
	_ = store.UpdateTransferProgress(paused, 4)
	_ = store.SetTransferState(paused, "paused", nil)
	fresh, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/f", Dst: filepath.Join(dir, "fresh.bin"), Size: 8})
	done, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/d", Dst: filepath.Join(dir, "done.bin"), Size: 8})
	_ = store.SetTransferState(done, "completed", nil)
	sink := &recSink{}
	d := New(store, sink, nil)

	// Act
	n, err := d.CancelMany([]int64{paused, fresh, done})
	if err != nil {
		t.Fatal(err)
	}
	d.sweeps.Wait()

	// Assert
	if n != 2 {
		t.Fatalf("cancelled %d, want 2", n)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("cancelled paused row left its placeholder behind: %v", err)
	}
	if got, _ := store.TransferByID(paused); got.State != "cancelled" || got.BytesDone != 0 {
		t.Errorf("paused row = %q with %d bytes, want cancelled/0", got.State, got.BytesDone)
	}
	if got, _ := store.TransferByID(done); got.State != "completed" {
		t.Errorf("completed row became %q", got.State)
	}
	// One refresh before the placeholder sweep (rows read as cancelled at
	// once) and one after — bounded by the batch, never by the row count.
	if c := sink.count("queue:changed"); c != 2 {
		t.Errorf("queue:changed emitted %d times, want exactly 2 for the batch", c)
	}
}

func openStoreWithSite(t *testing.T) (*queue.Store, int64) {
	t.Helper()
	store, err := queue.Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	site, err := store.SaveSite(queue.Site{Name: "t", Protocol: "sftp", Host: "example.test", Username: "u"})
	if err != nil {
		t.Fatal(err)
	}
	return store, site
}

// fakeRunning registers a transfer as in flight exactly as pump would, and
// returns the context its engine would be watching.
func fakeRunning(t *testing.T, d *Dispatcher, tr queue.Transfer) context.Context {
	t.Helper()
	tctx, tcancel := context.WithCancel(context.Background())
	t.Cleanup(tcancel)
	d.mu.Lock()
	d.cancels[tr.ID] = tcancel
	d.slots[tr.ID] = 1
	d.activeDst[dstKey(tr)] = true
	d.perSite[tr.SiteID]++
	d.activeN++
	d.mu.Unlock()
	if won, _ := d.store.ClaimPending(tr.ID); !won {
		t.Fatalf("claim %d", tr.ID)
	}
	return tctx
}

func TestStoppingQueueClaimsNothing(t *testing.T) {
	// Arrange — a row requeued by shutdown must not be picked straight back
	// up during the grace period and started into a closing database.
	store, site := openStoreWithSite(t)
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: filepath.Join(t.TempDir(), "a"), Size: 10})
	d := New(store, nopSink{}, nil) // nil factory: any claim panics

	// Act
	d.Stop(time.Millisecond)
	d.pump(context.Background())

	// Assert
	if got, _ := store.TransferByID(id); got.State != "pending" {
		t.Fatalf("row is %q after a pump during shutdown, want pending", got.State)
	}
}

func TestResumeBeforeLanesReleaseStillRequeuesClean(t *testing.T) {
	// Arrange — the queue is paused and resumed again before the running
	// transfer's engine has let go. The stop was still the pause's doing.
	store, site := openStoreWithSite(t)
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: "/l/a", Size: 10})
	d := New(store, nopSink{}, nil)
	tr := queue.Transfer{ID: id, SiteID: site, Src: "/a", Dst: "/l/a"}
	tctx := fakeRunning(t, d, tr)

	// Act — pause, resume immediately, then the engine unwinds.
	if err := d.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if err := d.SetPaused(false); err != nil {
		t.Fatal(err)
	}
	<-tctx.Done()
	d.finishWithError(tctx, tr, context.Canceled)
	d.release(tr)

	// Assert — pending and clean, not failed with "context canceled".
	got, _ := store.TransferByID(id)
	if got.State != "pending" || got.Error != nil || got.Attempt != 0 {
		t.Fatalf("row = %q err %v attempt %d; want a clean pending", got.State, got.Error, got.Attempt)
	}
	d.mu.Lock()
	_, leaked := d.requeue[id]
	d.mu.Unlock()
	if leaked {
		t.Error("release left the requeue intent behind")
	}
}

func TestCancelDuringPauseUnwindStaysCancelled(t *testing.T) {
	// Arrange — the queue is paused; before the engine unwinds the user
	// cancels the row. Requeue must not resurrect it, and the classifier
	// must not overwrite it with a retry.
	store, site := openStoreWithSite(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "a.bin")
	part := dst + sftpfast.PartSuffix
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: dst, Size: 8})
	_ = store.UpdateTransferProgress(id, 4)
	d := New(store, nopSink{}, nil)
	tr := queue.Transfer{ID: id, SiteID: site, Src: "/a", Dst: dst, Direction: "download", Size: 8}
	tctx := fakeRunning(t, d, tr)
	if err := d.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	if marked, err := store.CancelByID([]int64{id}); err != nil || len(marked) != 1 {
		t.Fatalf("cancel: marked=%v err=%v", marked, err)
	}

	// Act — engine unwinds after the cancel landed.
	<-tctx.Done()
	d.finishWithError(tctx, tr, context.Canceled)
	d.release(tr)

	// Assert
	got, _ := store.TransferByID(id)
	if got.State != "cancelled" || got.BytesDone != 0 {
		t.Fatalf("row = %q with %d bytes; want cancelled with its data discarded", got.State, got.BytesDone)
	}
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("placeholder survived the cancel: %v", err)
	}
}

func TestCancelQueuedLeavesAJustClaimedRowRunning(t *testing.T) {
	// Arrange — one row the pump has already claimed, one still waiting.
	// The dialog promised running transfers keep going; this one must.
	store, site := openStoreWithSite(t)
	claimed, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: "/l/a", Size: 10})
	waiting, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/b", Dst: "/l/b", Size: 10})
	d := New(store, &recSink{}, nil)
	tctx := fakeRunning(t, d, queue.Transfer{ID: claimed, SiteID: site, Src: "/a", Dst: "/l/a"})

	// Act
	n, err := d.CancelQueued()
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if n != 1 {
		t.Fatalf("cancelled %d rows, want 1 (the claimed one went active)", n)
	}
	if tctx.Err() != nil {
		t.Fatal("the running transfer was cut off by Cancel all queued")
	}
	if got, _ := store.TransferByID(claimed); got.State != "active" {
		t.Errorf("claimed row is %q, want active", got.State)
	}
	if got, _ := store.TransferByID(waiting); got.State != "cancelled" {
		t.Errorf("waiting row is %q, want cancelled", got.State)
	}
}

func TestCancelQueuedSparesAPlaceholderARunningSiblingIsWriting(t *testing.T) {
	// Arrange — an idle paused row with progress and a running row share a
	// destination. Cancelling the queue must discard the idle row's
	// bookkeeping but leave the file the running lanes are writing.
	store, site := openStoreWithSite(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "shared.bin")
	part := dst + sftpfast.PartSuffix
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	idle, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: dst, Size: 8})
	_ = store.UpdateTransferProgress(idle, 4)
	_ = store.SetTransferState(idle, "paused", nil)
	live, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/b", Dst: dst, Size: 8})
	d := New(store, &recSink{}, nil)
	fakeRunning(t, d, queue.Transfer{ID: live, SiteID: site, Src: "/b", Dst: dst})

	// Act
	if _, err := d.CancelQueued(); err != nil {
		t.Fatal(err)
	}
	d.sweeps.Wait()

	// Assert
	if _, err := os.Stat(part); err != nil {
		t.Fatalf("placeholder a running transfer is writing was removed: %v", err)
	}
	if got, _ := store.TransferByID(idle); got.State != "cancelled" {
		t.Errorf("idle row is %q, want cancelled", got.State)
	}
}

func TestReleaseDiscardsARowCancelledDuringItsClaim(t *testing.T) {
	// Arrange — the row is registered as running (pump does this before it
	// claims) and is cancelled in that window; the claim then loses and
	// release runs with no goroutine behind it.
	store, site := openStoreWithSite(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "a.bin")
	part := dst + sftpfast.PartSuffix
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: dst, Size: 8})
	_ = store.UpdateTransferProgress(id, 4)
	d := New(store, &recSink{}, nil)
	tr := queue.Transfer{ID: id, SiteID: site, Src: "/a", Dst: dst, Direction: "download", Size: 8}
	_, tcancel := context.WithCancel(context.Background())
	defer tcancel()
	d.mu.Lock()
	d.cancels[id] = tcancel
	d.slots[id] = 1
	d.activeDst[dstKey(tr)] = true
	d.activeN = 1
	d.mu.Unlock()

	// Act — cancel lands (sees "running", leaves the discard to a goroutine
	// that will never exist), then the claim loses and releases.
	if _, err := d.CancelMany([]int64{id}); err != nil {
		t.Fatal(err)
	}
	if won, _ := store.ClaimPending(id); won {
		t.Fatal("claim won against a cancelled row")
	}
	d.release(tr)
	d.sweeps.Wait()

	// Assert
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("placeholder survived a cancel that raced the claim: %v", err)
	}
	if got, _ := store.TransferByID(id); got.State != "cancelled" || got.BytesDone != 0 {
		t.Errorf("row = %q with %d bytes, want cancelled/0", got.State, got.BytesDone)
	}
}

func TestCancelManyDiscardsSharedDestinationPlaceholders(t *testing.T) {
	// Arrange — two paused rows aimed at one local file (different sources:
	// a conflict, not a duplicate), sharing one placeholder on disk. Both
	// cancelled in one batch: neither may treat the other as a live owner.
	store, site := openStoreWithSite(t)
	dir := t.TempDir()
	dst := filepath.Join(dir, "shared.bin")
	part := dst + sftpfast.PartSuffix
	if err := os.WriteFile(part, []byte("half"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: dst, Size: 8})
	b, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/b", Dst: dst, Size: 8})
	for _, id := range []int64{a, b} {
		_ = store.UpdateTransferProgress(id, 4)
		_ = store.SetTransferState(id, "paused", nil)
	}
	d := New(store, nopSink{}, nil)

	// Act
	if _, err := d.CancelMany([]int64{a, b}); err != nil {
		t.Fatal(err)
	}
	d.sweeps.Wait()

	// Assert
	if _, err := os.Stat(part); !os.IsNotExist(err) {
		t.Errorf("shared placeholder survived a batch cancel of both owners: %v", err)
	}
}

func TestPauseUnwindWithRowAlreadyPendingStaysPending(t *testing.T) {
	// Arrange — queue paused; before the engine lets go the user presses
	// the row's own Pause then Resume, so the row is 'pending' at unwind.
	store, site := openStoreWithSite(t)
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: "/l/a", Size: 10})
	d := New(store, nopSink{}, nil)
	tr := queue.Transfer{ID: id, SiteID: site, Src: "/a", Dst: "/l/a"}
	tctx := fakeRunning(t, d, tr)
	if err := d.SetPaused(true); err != nil {
		t.Fatal(err)
	}
	_ = d.Pause(id)
	_ = d.Resume(id)

	// Act
	<-tctx.Done()
	d.finishWithError(tctx, tr, context.Canceled)
	d.release(tr)

	// Assert — never "failed: context canceled".
	got, _ := store.TransferByID(id)
	if got.State != "pending" || got.Error != nil {
		t.Fatalf("row = %q err %v, want pending with no error", got.State, got.Error)
	}
}

func TestLateCancelDoesNotUnfinishACompletedTransfer(t *testing.T) {
	// Arrange — the cancel lands after the engine has renamed the finished
	// file into place but before the dispatcher records completion.
	store, site := openStoreWithSite(t)
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: filepath.Join(t.TempDir(), "a"), Size: 10})
	d := New(store, nopSink{}, nil)
	tr := queue.Transfer{ID: id, SiteID: site, Src: "/a", Dst: "/l/a"}
	fakeRunning(t, d, tr)
	if _, err := store.CancelByID([]int64{id}); err != nil {
		t.Fatal(err)
	}

	// Act — the success path's write is deliberately unguarded.
	if err := store.SetTransferState(id, "completed", nil); err != nil {
		t.Fatal(err)
	}
	d.release(tr)
	d.sweeps.Wait()

	// Assert — completed, with its bytes, and nothing discarded.
	if got, _ := store.TransferByID(id); got.State != "completed" {
		t.Errorf("row is %q, want completed: the file is on disk", got.State)
	}
}

func TestResumeHealsAnOrphanedActiveRow(t *testing.T) {
	// Arrange — a row left 'active' with no goroutine (a requeue that
	// failed to write during the pause), beside one still unwinding.
	store, site := openStoreWithSite(t)
	orphan, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/a", Dst: "/l/a", Size: 10})
	_ = store.SetTransferState(orphan, "active", nil)
	live, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "download", Src: "/b", Dst: "/l/b", Size: 10})
	d := New(store, nopSink{}, nil)
	fakeRunning(t, d, queue.Transfer{ID: live, SiteID: site, Src: "/b", Dst: "/l/b"})
	_ = d.SetPaused(true)

	// Act
	if err := d.SetPaused(false); err != nil {
		t.Fatal(err)
	}

	// Assert
	if got, _ := store.TransferByID(orphan); got.State != "pending" {
		t.Errorf("orphan is %q after resume, want pending", got.State)
	}
	if got, _ := store.TransferByID(live); got.State != "active" {
		t.Errorf("row still unwinding became %q", got.State)
	}
}

func TestDiscardSkipsRemoteDialDuringShutdown(t *testing.T) {
	// Arrange — a cancelled upload with progress, whose discard would need
	// a connection; the dispatcher is stopping. The nil factory makes any
	// dial a panic, which is the failure this test exists to catch.
	store, site := openStoreWithSite(t)
	id, _ := store.EnqueueTransfer(queue.Transfer{SiteID: site, Engine: "sftpfast", Direction: "upload", Src: "/l/a", Dst: "/r/a", Size: 10})
	_ = store.UpdateTransferProgress(id, 5)
	if _, err := store.CancelByID([]int64{id}); err != nil {
		t.Fatal(err)
	}
	d := New(store, nopSink{}, nil)
	d.Stop(time.Millisecond)

	// Act
	d.discardPartials(queue.Transfer{ID: id, SiteID: site, Direction: "upload", Src: "/l/a", Dst: "/r/a", Size: 10}, nil)

	// Assert — bytes still recorded, so Clear done knows there is data.
	if got, _ := store.TransferByID(id); got.BytesDone != 5 {
		t.Fatalf("shutdown discard reset progress to %d; want the record kept for Clear done", got.BytesDone)
	}
}

func TestBulkCancelDialsOncePerSiteNotOncePerRow(t *testing.T) {
	// Arrange — six part-uploaded rows on one unreachable site, cancelled
	// together. Dialling per row would wait out a separate timeout for
	// every one of them.
	store, site := openStoreWithSite(t)
	var dials int32
	d := New(store, nopSink{}, func(context.Context, int64, int) ([]*sftpfast.Client, error) {
		atomic.AddInt32(&dials, 1)
		return nil, fmt.Errorf("seedbox not answering")
	})
	ids := make([]int64, 0, 6)
	for i := 0; i < 6; i++ {
		id, _ := store.EnqueueTransfer(queue.Transfer{
			SiteID: site, Engine: "sftpfast", Direction: "upload",
			Src: fmt.Sprintf("/l/f%d", i), Dst: fmt.Sprintf("/r/f%d", i), Size: 10,
		})
		_ = store.UpdateTransferProgress(id, 5)
		ids = append(ids, id)
	}

	// Act
	n, err := d.CancelMany(ids)
	if err != nil {
		t.Fatal(err)
	}
	d.sweeps.Wait()

	// Assert
	if n != len(ids) {
		t.Fatalf("cancelled %d rows, want %d", n, len(ids))
	}
	if got := atomic.LoadInt32(&dials); got != 1 {
		t.Fatalf("dialled %d times for %d rows on one site, want 1", got, len(ids))
	}
	// Nothing was removed, so nothing may claim to have been: the rows keep
	// their byte counts for Clear done to sweep when the site is back.
	for _, id := range ids {
		got, _ := store.TransferByID(id)
		if got.BytesDone != 5 {
			t.Fatalf("row %d reports %d bytes after an unreachable cleanup, want 5 kept", id, got.BytesDone)
		}
	}
}
