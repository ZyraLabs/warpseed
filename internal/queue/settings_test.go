package queue

import "testing"

func TestSettingsDefaultsSeededByMigration(t *testing.T) {
	// Arrange & Act
	s := openTestStore(t)

	// Assert — migration 002 seeds transfer/bandwidth/theme defaults
	if got := s.SettingInt("transfers.global_max", 0); got != 6 {
		t.Fatalf("transfers.global_max = %d, want seeded 6", got)
	}
	if got := s.Setting("bw.mode", ""); got != "off" {
		t.Fatalf("bw.mode = %q, want off", got)
	}
	if got := s.Setting("ui.theme", ""); got != "dark" {
		t.Fatalf("ui.theme = %q, want dark", got)
	}
}

func TestSetSettingUpsertsAndReads(t *testing.T) {
	// Arrange
	s := openTestStore(t)

	// Act
	if err := s.SetSetting("ui.theme", "light"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("custom.key", "42"); err != nil {
		t.Fatal(err)
	}

	// Assert
	if got := s.Setting("ui.theme", ""); got != "light" {
		t.Fatalf("updated theme = %q, want light", got)
	}
	if got := s.SettingInt("custom.key", 0); got != 42 {
		t.Fatalf("custom int = %d, want 42", got)
	}
	all, err := s.AllSettings()
	if err != nil {
		t.Fatal(err)
	}
	if all["custom.key"] != "42" {
		t.Fatalf("AllSettings missing upsert: %v", all)
	}
}

func TestSettingIntMalformedFallsBack(t *testing.T) {
	// Arrange
	s := openTestStore(t)
	s.SetSetting("bad.int", "not-a-number")

	// Act & Assert
	if got := s.SettingInt("bad.int", 7); got != 7 {
		t.Fatalf("malformed int = %d, want fallback 7", got)
	}
}

func TestSiteRoundTripsNewColumns(t *testing.T) {
	// Arrange
	s := openTestStore(t)

	// Act — migration 002 columns flow through save + fetch
	id, err := s.SaveSite(Site{
		Name: "box", Protocol: "sftp", Host: "example.test", Username: "u",
		RemotePath: "/downloads/complete", MaxTransfers: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.SiteByID(id)
	if err != nil {
		t.Fatal(err)
	}

	// Assert
	if got.RemotePath != "/downloads/complete" || got.MaxTransfers != 5 {
		t.Fatalf("new columns lost: %+v", got)
	}
}
