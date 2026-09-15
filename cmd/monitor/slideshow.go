package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
)

const (
	slideshowRefreshInterval = time.Minute
	autoAlbumScanTimeout     = 2 * time.Minute
	maxSlideshowImages       = 50000
)

type slideshowImage struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Modified int64  `json:"modified"`
}

type slideshowManifest struct {
	Directories []string         `json:"directories"`
	Images      []slideshowImage `json:"images"`
	Scanning    bool             `json:"scanning"`
	Truncated   bool             `json:"truncated"`
	Error       string           `json:"error,omitempty"`
	GeneratedAt int64            `json:"generated_at"`
}

var slideshowLibrary = struct {
	sync.RWMutex
	manifest    slideshowManifest
	paths       map[string]string
	lastRefresh time.Time
}{paths: make(map[string]string)}

var errStopSlideshowWalk = errors.New("stop slideshow walk")

func isSlideshowImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".bmp":
		return true
	default:
		return false
	}
}

func slideshowImageID(path string) string {
	key := filepath.Clean(path)
	if os.PathSeparator == '\\' {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:16])
}

func scanSlideshowImages(dirs []string) ([]slideshowImage, map[string]string, bool) {
	images := make([]slideshowImage, 0)
	paths := make(map[string]string)
	seenPaths := make(map[string]struct{})
	truncated := false

	for _, root := range dirs {
		if truncated {
			break
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			if !entry.Type().IsRegular() || !isSlideshowImage(path) {
				return nil
			}
			absolute, err := filepath.Abs(path)
			if err != nil {
				return nil
			}
			key := filepath.Clean(absolute)
			if os.PathSeparator == '\\' {
				key = strings.ToLower(key)
			}
			if _, exists := seenPaths[key]; exists {
				return nil
			}
			info, err := entry.Info()
			if err != nil {
				return nil
			}
			id := slideshowImageID(absolute)
			seenPaths[key] = struct{}{}
			paths[id] = absolute
			images = append(images, slideshowImage{
				ID:       id,
				Name:     filepath.Base(absolute),
				URL:      "/api/slideshow/image?id=" + id + "&v=" + strconv.FormatInt(info.ModTime().Unix(), 10),
				Size:     info.Size(),
				Modified: info.ModTime().Unix(),
			})
			if len(images) >= maxSlideshowImages {
				truncated = true
				return errStopSlideshowWalk
			}
			return nil
		})
	}

	sort.Slice(images, func(i, j int) bool {
		return strings.ToLower(paths[images[i].ID]) < strings.ToLower(paths[images[j].ID])
	})
	return images, paths, truncated
}

var albumScanSkipDirs = map[string]struct{}{
	"$recycle.bin":              {},
	"system volume information": {},
	"windows":                   {},
	"program files":             {},
	"program files (x86)":       {},
	"programdata":               {},
	"node_modules":              {},
	".git":                      {},
	".gradle":                   {},
	".gocache":                  {},
}

func findAlbumDirectories(roots []string, deadline time.Time) []string {
	found := make([]string, 0)
	seen := make(map[string]struct{})
	for _, root := range normalizeSlideshowDirs(roots) {
		if time.Now().After(deadline) {
			break
		}
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if time.Now().After(deadline) {
				return errStopSlideshowWalk
			}
			if walkErr != nil {
				return nil
			}
			if !entry.IsDir() {
				return nil
			}
			name := entry.Name()
			if path != root && strings.Contains(name, "相册") {
				absolute, err := filepath.Abs(path)
				if err == nil {
					key := absolute
					if os.PathSeparator == '\\' {
						key = strings.ToLower(key)
					}
					if _, exists := seen[key]; !exists {
						seen[key] = struct{}{}
						found = append(found, absolute)
					}
				}
				return filepath.SkipDir
			}
			if _, skip := albumScanSkipDirs[strings.ToLower(name)]; skip && path != root {
				return filepath.SkipDir
			}
			return nil
		})
	}
	sort.Slice(found, func(i, j int) bool { return strings.ToLower(found[i]) < strings.ToLower(found[j]) })
	return found
}

// findAlbumDirectoriesConcurrently starts every volume at the same time. A slow
// system volume therefore cannot consume the whole deadline before later drive
// letters (for example E:) have even been visited.
func findAlbumDirectoriesConcurrently(roots []string, timeout time.Duration) []string {
	roots = normalizeSlideshowDirs(roots)
	if len(roots) == 0 {
		return nil
	}
	if timeout <= 0 {
		timeout = autoAlbumScanTimeout
	}
	deadline := time.Now().Add(timeout)
	results := make(chan []string, len(roots))
	for _, root := range roots {
		go func(root string) {
			results <- findAlbumDirectories([]string{root}, deadline)
		}(root)
	}

	found := make([]string, 0)
	for range roots {
		found = append(found, <-results...)
	}
	return normalizeAndSortSlideshowDirs(found)
}

func normalizeAndSortSlideshowDirs(dirs []string) []string {
	dirs = normalizeSlideshowDirs(dirs)
	sort.Slice(dirs, func(i, j int) bool { return strings.ToLower(dirs[i]) < strings.ToLower(dirs[j]) })
	return dirs
}

func mergeSlideshowDirs(existing, discovered []string) []string {
	return normalizeSlideshowDirs(append(append([]string(nil), existing...), discovered...))
}

func slideshowSearchRoots() []string {
	partitions, err := disk.Partitions(false)
	if err != nil {
		return nil
	}
	roots := make([]string, 0, len(partitions))
	for _, partition := range partitions {
		if partition.Mountpoint != "" {
			roots = append(roots, partition.Mountpoint)
		}
	}
	return normalizeSlideshowDirs(roots)
}

func refreshSlideshowLibraryAsync(force bool) {
	slideshowLibrary.Lock()
	if slideshowLibrary.manifest.Scanning || (!force && time.Since(slideshowLibrary.lastRefresh) < slideshowRefreshInterval) {
		slideshowLibrary.Unlock()
		return
	}
	slideshowLibrary.manifest.Scanning = true
	slideshowLibrary.manifest.Error = ""
	slideshowLibrary.Unlock()

	go func() {
		dirs, err := loadSlideshowDirs()

		var images []slideshowImage
		paths := make(map[string]string)
		truncated := false
		if err == nil && len(dirs) > 0 {
			images, paths, truncated = scanSlideshowImages(dirs)
		}
		errorText := ""
		if err != nil {
			errorText = err.Error()
		}
		slideshowLibrary.Lock()
		slideshowLibrary.manifest = slideshowManifest{
			Directories: append([]string(nil), dirs...),
			Images:      images,
			Scanning:    false,
			Truncated:   truncated,
			Error:       errorText,
			GeneratedAt: time.Now().Unix(),
		}
		slideshowLibrary.paths = paths
		slideshowLibrary.lastRefresh = time.Now()
		slideshowLibrary.Unlock()
	}()
}

func invalidateSlideshowLibrary() {
	slideshowLibrary.Lock()
	slideshowLibrary.lastRefresh = time.Time{}
	slideshowLibrary.Unlock()
	refreshSlideshowLibraryAsync(true)
}

func handleSlideshowManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkAccess(r) {
		http.Error(w, "access denied by host", http.StatusForbidden)
		return
	}
	refreshSlideshowLibraryAsync(false)
	slideshowLibrary.RLock()
	manifest := slideshowLibrary.manifest
	// JSON 始终返回数组而不是 null，兼容旧 Android 客户端的数组解析。
	manifest.Directories = append([]string{}, manifest.Directories...)
	manifest.Images = append([]slideshowImage{}, manifest.Images...)
	slideshowLibrary.RUnlock()
	writeJSON(w, manifest)
}

func handleSlideshowImage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !checkAccess(r) {
		http.Error(w, "access denied by host", http.StatusForbidden)
		return
	}
	id := r.URL.Query().Get("id")
	slideshowLibrary.RLock()
	path := slideshowLibrary.paths[id]
	slideshowLibrary.RUnlock()
	if path == "" {
		http.NotFound(w, r)
		return
	}
	file, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	if contentType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeContent(w, r, info.Name(), info.ModTime(), file)
}
