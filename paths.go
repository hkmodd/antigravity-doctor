package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
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
