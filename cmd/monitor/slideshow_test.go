package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanSlideshowImagesRecursivelyAndDeduplicates(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(root, "one.jpg"),
		filepath.Join(nested, "two.PNG"),
		filepath.Join(nested, "ignore.txt"),
	} {
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	images, paths, truncated := scanSlideshowImages([]string{root, nested})
	if truncated {
		t.Fatal("unexpected truncation")
	}
	if len(images) != 2 || len(paths) != 2 {
		t.Fatalf("got %d images and %d paths, want 2", len(images), len(paths))
	}
}

func TestFindAlbumDirectories(t *testing.T) {
	root := t.TempDir()
	want := filepath.Join(root, "家庭相册")
	if err := os.MkdirAll(filepath.Join(want, "child"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "ordinary"), 0o755); err != nil {
		t.Fatal(err)
	}
	got := findAlbumDirectories([]string{root}, time.Now().Add(time.Second))
	if len(got) != 1 || got[0] != want {
		t.Fatalf("directories = %#v, want [%q]", got, want)
	}
}

func TestFindAlbumDirectoriesConcurrentlyIncludesEveryRoot(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	wantFirst := filepath.Join(first, "工作相册")
	wantSecond := filepath.Join(second, "家庭相册")
	for _, path := range []string{wantFirst, wantSecond} {
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	got := findAlbumDirectoriesConcurrently([]string{first, second}, time.Second)
	found := make(map[string]bool, len(got))
	for _, path := range got {
		found[path] = true
	}
	if len(got) != 2 || !found[wantFirst] || !found[wantSecond] {
		t.Fatalf("directories = %#v, want both %q and %q", got, wantFirst, wantSecond)
	}
}

func TestMergeSlideshowDirsKeepsExistingOrderAndAddsUniqueDirectories(t *testing.T) {
	got := mergeSlideshowDirs([]string{`D:\相册`}, []string{`D:\相册`, `E:\相册`})
	if len(got) != 2 || got[0] != `D:\相册` || got[1] != `E:\相册` {
		t.Fatalf("merged directories = %#v", got)
	}
}
