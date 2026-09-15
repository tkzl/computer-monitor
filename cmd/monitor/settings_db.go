package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	_ "modernc.org/sqlite"
)

const monitorDBName = "monitor.db"

var monitorSettingsDB *sql.DB

// monitorDBPath returns the persistent database beside monitor.exe. During
// `go run`, the executable lives below Go's temporary go-build directory, so
// use the current project directory instead.
func monitorDBPath() string {
	if custom := strings.TrimSpace(os.Getenv("MONITOR_DB")); custom != "" {
		return custom
	}
	exe, err := os.Executable()
	if err == nil {
		cleanExe := strings.ToLower(filepath.Clean(exe))
		if !strings.Contains(cleanExe, string(os.PathSeparator)+"go-build") {
			return filepath.Join(filepath.Dir(exe), monitorDBName)
		}
	}
	return monitorDBName
}

func openSettingsDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("打开设置数据库: %w", err)
	}
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS slideshow_directories (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			path       TEXT NOT NULL UNIQUE,
			sort_order INTEGER NOT NULL,
			created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("初始化幻灯片目录表: %w", err)
	}
	return db, nil
}

func initSettingsDB() error {
	db, err := openSettingsDB(monitorDBPath())
	if err != nil {
		return err
	}
	monitorSettingsDB = db
	return nil
}

func normalizeSlideshowDirs(dirs []string) []string {
	result := make([]string, 0, len(dirs))
	seen := make(map[string]struct{}, len(dirs))
	for _, raw := range dirs {
		path := strings.TrimSpace(raw)
		if path == "" {
			continue
		}
		path = filepath.Clean(path)
		key := path
		if os.PathSeparator == '\\' {
			key = strings.ToLower(path)
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, path)
	}
	return result
}

func loadSlideshowDirs() ([]string, error) {
	if monitorSettingsDB == nil {
		return nil, fmt.Errorf("设置数据库尚未初始化")
	}
	rows, err := monitorSettingsDB.Query(`
		SELECT path
		FROM slideshow_directories
		ORDER BY sort_order, id`)
	if err != nil {
		return nil, fmt.Errorf("读取幻灯片目录: %w", err)
	}
	defer rows.Close()

	var dirs []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("读取幻灯片目录: %w", err)
		}
		dirs = append(dirs, path)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("读取幻灯片目录: %w", err)
	}
	return dirs, nil
}

func saveSlideshowDirs(dirs []string) error {
	if monitorSettingsDB == nil {
		return fmt.Errorf("设置数据库尚未初始化")
	}
	dirs = normalizeSlideshowDirs(dirs)
	tx, err := monitorSettingsDB.Begin()
	if err != nil {
		return fmt.Errorf("开始保存幻灯片目录: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM slideshow_directories`); err != nil {
		return fmt.Errorf("清空旧幻灯片目录: %w", err)
	}
	stmt, err := tx.Prepare(`
		INSERT INTO slideshow_directories(path, sort_order)
		VALUES (?, ?)`)
	if err != nil {
		return fmt.Errorf("准备保存幻灯片目录: %w", err)
	}
	defer stmt.Close()
	for i, path := range dirs {
		if _, err := stmt.Exec(path, i); err != nil {
			return fmt.Errorf("保存幻灯片目录 %q: %w", path, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("提交幻灯片目录设置: %w", err)
	}
	return nil
}
