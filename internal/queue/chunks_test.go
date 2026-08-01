package queue

import "testing"

func TestPlanChunksCoversRangeExactly(t *testing.T) {
	// Arrange — a size that does not divide evenly
	const size = 1_000_003

	// Act
	chunks := PlanChunks(7, size, 4)

	// Assert — contiguous, no gaps or overlaps, last absorbs the remainder
	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4", len(chunks))
	}
	var covered int64
	for i, c := range chunks {
		if c.Idx != i {
			t.Fatalf("chunk %d has idx %d", i, c.Idx)
		}
		if c.Offset != covered {
			t.Fatalf("chunk %d starts at %d, want %d (gap or overlap)", i, c.Offset, covered)
		}
		covered += c.Length
	}
	if covered != size {
		t.Fatalf("chunks cover %d bytes, want %d", covered, size)
	}
}

func TestPlanChunksDeclinesWhenNotWorthwhile(t *testing.T) {
	// Act & Assert — fewer than 2 streams, or chunks smaller than 1 byte
	if PlanChunks(1, 1000, 1) != nil {
		t.Fatal("single stream should not produce a chunk plan")
	}
	if PlanChunks(1, 3, 8) != nil {
		t.Fatal("size smaller than stream count should not produce a plan")
	}
	if PlanChunks(1, 0, 4) != nil {
		t.Fatal("zero size should not produce a plan")
	}
}

func TestChunkPersistenceRoundTrip(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/big.bin", Dst: "/l/big.bin", Size: 800})
	plan := PlanChunks(id, 800, 4)

	// Act
	if err := s.SaveChunks(id, plan); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateChunkProgress(id, 2, 150, "active"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Chunks(id)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if len(got) != 4 {
		t.Fatalf("got %d chunks, want 4", len(got))
	}
	if got[2].BytesDone != 150 || got[2].State != "active" {
		t.Fatalf("checkpoint lost: %+v", got[2])
	}
	if got[0].Offset != 0 || got[1].Offset != 200 {
		t.Fatalf("offsets wrong: %+v", got[:2])
	}
}

func TestSaveChunksReplacesPriorPlan(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/b", Dst: "/l/b", Size: 800})
	if err := s.SaveChunks(id, PlanChunks(id, 800, 8)); err != nil {
		t.Fatal(err)
	}

	// Act — re-plan with fewer streams
	if err := s.SaveChunks(id, PlanChunks(id, 800, 2)); err != nil {
		t.Fatal(err)
	}

	// Assert — no orphaned rows from the old plan
	got, _ := s.Chunks(id)
	if len(got) != 2 {
		t.Fatalf("got %d chunks after re-plan, want 2", len(got))
	}
}

func TestDeleteChunksCascadesWithTransfer(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	id, _ := s.EnqueueTransfer(Transfer{SiteID: site, Src: "/c", Dst: "/l/c", Size: 400})
	s.SaveChunks(id, PlanChunks(id, 400, 2))

	// Act
	if err := s.DeleteChunks(id); err != nil {
		t.Fatal(err)
	}

	// Assert
	got, _ := s.Chunks(id)
	if len(got) != 0 {
		t.Fatalf("chunks survived delete: %+v", got)
	}
}
