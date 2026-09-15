package main

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestSlideshowDirectoriesRoundTrip(t *testing.T) {
	db, err := openSettingsDB(filepath.Join(t.TempDir(), "settings.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	previous := monitorSettingsDB
	monitorSettingsDB = db
	defer func() { monitorSettingsDB = previous }()

	want := []string{filepath.Clean(`D:\Photos`), filepath.Clean(`E:\Family`)}
	input := []string{` D:\Photos `, `E:\Family`, `d:\photos`, ""}
	if err := saveSlideshowDirs(input); err != nil {
		t.Fatal(err)
	}
	got, err := loadSlideshowDirs()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("directories = %#v, want %#v", got, want)
	}
}
