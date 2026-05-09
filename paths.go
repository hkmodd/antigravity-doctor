package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Paths holds all the filesystem paths Antigravity uses
type Paths struct {
	GeminiDir           string
	ConversationsDir    string
	BrainDir            string
	AnnotationsDir      string
	DBPath              string
	WorkspaceStorageDir string
}

func detectPaths() Paths {
	home, _ := os.UserHomeDir()
	var p Paths

	p.GeminiDir = filepath.Join(home, ".gemini", "antigravity")
	p.ConversationsDir = filepath.Join(p.GeminiDir, "conversations")
	p.BrainDir = filepath.Join(p.GeminiDir, "brain")
	p.AnnotationsDir = filepath.Join(p.GeminiDir, "annotations")

	switch runtime.GOOS {
	case "windows":
		appdata := os.Getenv("APPDATA")
		p.DBPath = filepath.Join(appdata, "Antigravity", "User", "globalStorage", "state.vscdb")
		p.WorkspaceStorageDir = filepath.Join(appdata, "Antigravity", "User", "workspaceStorage")
	case "darwin":
		p.DBPath = filepath.Join(home, "Library", "Application Support", "antigravity", "User", "globalStorage", "state.vscdb")
		p.WorkspaceStorageDir = filepath.Join(home, "Library", "Application Support", "antigravity", "User", "workspaceStorage")
	default: // Linux
		p.DBPath = filepath.Join(home, ".config", "Antigravity", "User", "globalStorage", "state.vscdb")
		p.WorkspaceStorageDir = filepath.Join(home, ".config", "Antigravity", "User", "workspaceStorage")
	}
	return p
}

func (p Paths) Validate() error {
	if _, err := os.Stat(p.DBPath); err != nil {
		return fmt.Errorf("database not found: %s", p.DBPath)
	}
	if _, err := os.Stat(p.ConversationsDir); err != nil {
		return fmt.Errorf("conversations dir not found: %s", p.ConversationsDir)
	}
	return nil
}

// ConvFile represents a conversation .pb file on disk
type ConvFile struct {
	ID    string
	Path  string
	Mtime float64
}

func discoverConversations(dir string) ([]ConvFile, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var convs []ConvFile
	for _, f := range files {
		name := f.Name()
		if !strings.HasSuffix(name, ".pb") || strings.HasSuffix(name, ".tmp") {
			continue
		}
		id := strings.TrimSuffix(name, ".pb")
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		convs = append(convs, ConvFile{
			ID:    id,
			Path:  path,
			Mtime: float64(info.ModTime().Unix()),
		})
	}

	// Sort newest first
	sort.Slice(convs, func(i, j int) bool {
		return convs[i].Mtime > convs[j].Mtime
	})
	return convs, nil
}

// AnnotationStats holds annotation health info
type AnnotationStats struct {
	Ghosts    int
	Orphans   int
	OrphanIDs []string
}

func checkAnnotations(annDir string, convs []ConvFile) AnnotationStats {
	stats := AnnotationStats{}
	if annDir == "" {
		return stats
	}
	annIDs := map[string]bool{}
	files, err := os.ReadDir(annDir)
	if err != nil {
		return stats
	}
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".pbtxt") {
			annIDs[strings.TrimSuffix(f.Name(), ".pbtxt")] = true
		}
	}
	convIDs := map[string]bool{}
	for _, c := range convs {
		convIDs[c.ID] = true
	}
	for id := range annIDs {
		if !convIDs[id] {
			stats.Ghosts++
		}
	}
	for id := range convIDs {
		if !annIDs[id] {
			stats.Orphans++
			stats.OrphanIDs = append(stats.OrphanIDs, id)
		}
	}
	return stats
}

func cleanTempFiles(dir string) int {
	files, _ := os.ReadDir(dir)
	cleaned := 0
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".tmp") {
			os.Remove(filepath.Join(dir, f.Name()))
			cleaned++
		}
	}
	return cleaned
}

func createMissingAnnotations(annDir string, orphanIDs []string, convDir string) int {
	if annDir == "" {
		return 0
	}
	created := 0
	for _, id := range orphanIDs {
		path := filepath.Join(annDir, id+".pbtxt")
		if _, err := os.Stat(path); err == nil {
			continue
		}
		pbPath := filepath.Join(convDir, id+".pb")
		info, err := os.Stat(pbPath)
		mtime := time.Now()
		if err == nil {
			mtime = info.ModTime()
		}
		content := fmt.Sprintf("last_user_view_time:{seconds:%d  nanos:0}\n", mtime.Unix())
		os.WriteFile(path, []byte(content), 0644)
		created++
	}
	return created
}
