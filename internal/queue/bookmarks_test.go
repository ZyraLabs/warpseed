package queue

import "testing"

func TestBookmarksScopedBySource(t *testing.T) {
	// Arrange — the same path string on the local pane and on a site must
	// not collide (0 is the local filesystem).
	s := openTestStore(t)
	site := seedSite(t, s)

	// Act
	if _, err := s.AddBookmark(0, "/data/media", "local media"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBookmark(site, "/data/media", "seedbox media"); err != nil {
		t.Fatal(err)
	}

	// Assert
	local, _ := s.Bookmarks(0)
	remote, _ := s.Bookmarks(site)
	if len(local) != 1 || local[0].Label != "local media" {
		t.Fatalf("local bookmarks wrong: %+v", local)
	}
	if len(remote) != 1 || remote[0].Label != "seedbox media" {
		t.Fatalf("remote bookmarks wrong: %+v", remote)
	}
}

func TestAddBookmarkTwiceUpdatesLabel(t *testing.T) {
	// Arrange
	s := openTestStore(t)

	// Act — re-bookmarking a folder should not error
	if _, err := s.AddBookmark(0, "/downloads", "old"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddBookmark(0, "/downloads", "new"); err != nil {
		t.Fatalf("re-bookmarking failed: %v", err)
	}

	// Assert — one row, updated label
	got, _ := s.Bookmarks(0)
	if len(got) != 1 || got[0].Label != "new" {
		t.Fatalf("expected one updated bookmark, got %+v", got)
	}
}

func TestDeleteBookmark(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	id, _ := s.AddBookmark(0, "/tmp/x", "x")

	// Act
	if err := s.DeleteBookmark(id); err != nil {
		t.Fatal(err)
	}

	// Assert
	got, _ := s.Bookmarks(0)
	if len(got) != 0 {
		t.Fatalf("bookmark survived: %+v", got)
	}
}

func TestBookmarksCascadeWithSite(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	site := seedSite(t, s)
	s.AddBookmark(site, "/downloads", "")

	// Act — deleting the site should not orphan its bookmarks
	if err := s.DeleteSite(site); err != nil {
		t.Fatal(err)
	}

	// Assert
	got, _ := s.Bookmarks(site)
	if len(got) != 0 {
		t.Fatalf("bookmarks survived their site: %+v", got)
	}
}
