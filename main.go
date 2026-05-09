package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const Version = "2.0.0"

// Resolved holds a fully resolved conversation entry
type Resolved struct {
	CID, Title, Source string
	Blob               []byte
	Workspace          string
	Mtime              float64
}

func main() {
	// Handle --clean-ghosts flag
	if len(os.Args) > 1 && os.Args[1] == "--clean-ghosts" {
		runCleanGhosts()
		return
	}

	fmt.Println()
	fmt.Println(strings.Repeat("=", 64))
	fmt.Printf("   Antigravity Doctor v%s\n", Version)
	fmt.Println("   Self-healing conversation index rebuilder")
	fmt.Println("   NO REBOOT REQUIRED")
	fmt.Println(strings.Repeat("=", 64))
	fmt.Println()

	if isAntigravityRunning() {
		fmt.Println("  WARNING: Antigravity is still running!")
		fmt.Println("  Close it first (File > Exit or Task Manager).")
		fmt.Println()
		fmt.Print("  Press Enter after closing (or Q to quit): ")
		var input string
		fmt.Scanln(&input)
		if strings.TrimSpace(strings.ToLower(input)) == "q" {
			return
		}
	}

	paths := detectPaths()
	if err := paths.Validate(); err != nil {
		fmt.Printf("  ERROR: %v\n", err)
		waitExit()
		return
	}

	convs, err := discoverConversations(paths.ConversationsDir)
	if err != nil || len(convs) == 0 {
		fmt.Println("  No conversations found on disk.")
		waitExit()
		return
	}
	fmt.Printf("  Found %d conversations on disk\n", len(convs))

	annStats := checkAnnotations(paths.AnnotationsDir, convs)
	fmt.Printf("  Annotations: %d ghosts, %d orphans\n", annStats.Ghosts, annStats.Orphans)

	cleaned := cleanTempFiles(paths.ConversationsDir)
	if cleaned > 0 {
		fmt.Printf("  Cleaned %d stale .tmp files\n", cleaned)
	}

	fmt.Println("  Reading existing metadata...")
	titles, blobs := extractExistingMetadata(paths.DBPath)
	fmt.Printf("  Preserved %d titles, %d metadata blobs\n", len(titles), len(blobs))

	knownWS := loadWorkspaceURIs(paths.WorkspaceStorageDir)
	fmt.Printf("  Loaded %d known workspaces\n", len(knownWS))
	fmt.Println()

	// Resolve all conversations
	fmt.Println("  Scanning conversations (newest first):")
	fmt.Println("  " + strings.Repeat("-", 60))

	var entries []Resolved
	stats := map[string]int{"brain": 0, "preserved": 0, "fallback": 0}
	markers := map[string]string{"brain": "+", "preserved": "~", "fallback": "?"}

	for i, c := range convs {
		title, src := resolveTitle(c.ID, titles, paths.BrainDir, c.Mtime)
		blob := blobs[c.ID]
		ws := inferWorkspace(c.ID, paths.BrainDir, knownWS)
		entries = append(entries, Resolved{c.ID, title, src, blob, ws, c.Mtime})
		stats[src]++

		wsFlag := ""
		if ws != "" {
			wsFlag = " [WS]"
		}
		dt := title
		if len(dt) > 50 {
			dt = dt[:50]
		}
		if i < 20 || i == len(convs)-1 {
			fmt.Printf("    [%3d] %s %s%s\n", i+1, markers[src], dt, wsFlag)
		} else if i == 20 {
			fmt.Printf("    ... (%d more) ...\n", len(convs)-20)
		}
	}

	fmt.Println("  " + strings.Repeat("-", 60))
	fmt.Printf("  [+] brain: %d  [~] preserved: %d  [?] fallback: %d\n",
		stats["brain"], stats["preserved"], stats["fallback"])
	fmt.Println()

	// Backup
	backupDir := filepath.Join(filepath.Dir(exePath()), "antigravity_backup")
	os.MkdirAll(backupDir, 0755)
	ts := time.Now().Format("20060102_150405")
	backupPath := filepath.Join(backupDir, fmt.Sprintf("state.vscdb.%s.bak", ts))
	copyFile(paths.DBPath, backupPath)
	fmt.Printf("  Backup saved: %s\n\n", backupPath)

	// FIX 1: trajectorySummaries
	fmt.Println("  [1/4] Rebuilding trajectorySummaries...")
	var trajBytes []byte
	for _, e := range entries {
		entry := buildTrajectoryEntry(e.CID, e.Title, e.Blob, e.Workspace, e.Mtime)
		trajBytes = append(trajBytes, encodeLengthDelimited(1, entry)...)
	}
	upsertDBKey(paths.DBPath, "antigravityUnifiedStateSync.trajectorySummaries",
		base64.StdEncoding.EncodeToString(trajBytes))
	fmt.Printf("       Done (%d entries)\n", len(entries))

	// FIX 2: ChatSessionStore.index
	fmt.Println("  [2/4] Rebuilding ChatSessionStore.index...")
	upsertDBKey(paths.DBPath, "chat.ChatSessionStore.index", buildChatIndex(entries))
	fmt.Printf("       Done (%d entries)\n", len(entries))

	// FIX 3: Workspace indices
	fmt.Println("  [3/4] Patching workspace indices...")
	wsPatch := fixWorkspaceIndices(paths.WorkspaceStorageDir, entries)
	fmt.Printf("       Patched %d workspace databases\n", wsPatch)

	// FIX 4: Missing annotations
	fmt.Println("  [4/4] Creating missing annotations...")
	created := createMissingAnnotations(paths.AnnotationsDir, annStats.OrphanIDs, paths.ConversationsDir)
	fmt.Printf("       Created %d annotations\n", created)

	fmt.Println()
	fmt.Println("  " + strings.Repeat("=", 60))
	fmt.Printf("  SUCCESS! Index rebuilt with %d conversations.\n", len(entries))
	fmt.Println("  " + strings.Repeat("=", 60))
	fmt.Println()
	fmt.Println("  NEXT: Close Antigravity if open, then reopen it.")
	fmt.Println("  NO REBOOT NEEDED!")
	fmt.Println()
	waitExit()
}

// === Helpers ===

func exePath() string {
	p, err := os.Executable()
	if err != nil {
		return "."
	}
	return p
}

func waitExit() {
	fmt.Print("  Press Enter to close...")
	fmt.Scanln()
}

func isAntigravityRunning() bool {
	if runtime.GOOS == "windows" {
		cmd := exec.Command("tasklist", "/FI", "IMAGENAME eq Antigravity.exe")
		hideConsoleWindow(cmd)
		out, err := cmd.Output()
		if err == nil && strings.Contains(strings.ToLower(string(out)), "antigravity.exe") {
			return true
		}
	} else {
		out, _ := exec.Command("pgrep", "-f", "antigravity").Output()
		if strings.TrimSpace(string(out)) != "" {
			return true
		}
	}
	return false
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func buildChatIndex(entries []Resolved) string {
	type chatEntry struct {
		IsActive  bool   `json:"isActive"`
		SessionID string `json:"sessionId"`
	}
	type chatIndex struct {
		Version int                  `json:"version"`
		Entries map[string]chatEntry `json:"entries"`
	}
	idx := chatIndex{Version: 1, Entries: make(map[string]chatEntry)}
	for _, e := range entries {
		idx.Entries[e.CID] = chatEntry{IsActive: false, SessionID: e.CID}
	}
	b, _ := json.Marshal(idx)
	return string(b)
}

func fixWorkspaceIndices(wsDir string, entries []Resolved) int {
	if wsDir == "" {
		return 0
	}
	dirs, err := os.ReadDir(wsDir)
	if err != nil {
		return 0
	}

	// Build URI->hash map and URI->conversations map
	uriToHash := map[string]string{}
	for _, d := range dirs {
		if !d.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(wsDir, d.Name(), "workspace.json"))
		if err != nil {
			continue
		}
		var ws map[string]interface{}
		if json.Unmarshal(data, &ws) == nil {
			if uri, ok := ws["folder"].(string); ok && uri != "" {
				uriToHash[uri] = d.Name()
			} else if uri, ok := ws["workspace"].(string); ok && uri != "" {
				uriToHash[uri] = d.Name()
			}
		}
	}

	// Group conversations by workspace URI
	wsConvs := map[string][]Resolved{}
	for _, e := range entries {
		if e.Workspace != "" {
			uri := pathToURI(e.Workspace)
			wsConvs[uri] = append(wsConvs[uri], e)
		}
	}

	// Write per-workspace indices
	type chatEntry struct {
		IsActive  bool   `json:"isActive"`
		SessionID string `json:"sessionId"`
	}
	type chatIndex struct {
		Version int                  `json:"version"`
		Entries map[string]chatEntry `json:"entries"`
	}

	fixed := 0
	for wsURI, convList := range wsConvs {
		hash, ok := uriToHash[wsURI]
		if !ok {
			continue
		}
		dbPath := filepath.Join(wsDir, hash, "state.vscdb")
		if _, err := os.Stat(dbPath); err != nil {
			continue
		}

		idx := chatIndex{Version: 1, Entries: make(map[string]chatEntry)}
		for _, c := range convList {
			idx.Entries[c.CID] = chatEntry{IsActive: false, SessionID: c.CID}
		}
		b, _ := json.Marshal(idx)
		upsertDBKey(dbPath, "chat.ChatSessionStore.index", string(b))
		fixed++
	}
	return fixed
}

func upsertDBKey(dbPath, key, value string) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return
	}
	defer db.Close()
	var existing string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key=?", key).Scan(&existing)
	if err == nil {
		db.Exec("UPDATE ItemTable SET value=? WHERE key=?", value, key)
	} else {
		db.Exec("INSERT INTO ItemTable (key, value) VALUES (?, ?)", key, value)
	}
}

func runCleanGhosts() {
	paths := detectPaths()
	fmt.Println("Cleaning ghost annotations...")
	if _, err := os.Stat(paths.AnnotationsDir); err != nil {
		fmt.Println("No annotations directory found.")
		return
	}
	convIDs := map[string]bool{}
	files, _ := os.ReadDir(paths.ConversationsDir)
	for _, f := range files {
		if strings.HasSuffix(f.Name(), ".pb") {
			convIDs[strings.TrimSuffix(f.Name(), ".pb")] = true
		}
	}
	cleaned := 0
	annFiles, _ := os.ReadDir(paths.AnnotationsDir)
	for _, f := range annFiles {
		if strings.HasSuffix(f.Name(), ".pbtxt") {
			id := strings.TrimSuffix(f.Name(), ".pbtxt")
			if !convIDs[id] {
				os.Remove(filepath.Join(paths.AnnotationsDir, f.Name()))
				cleaned++
			}
		}
	}
	fmt.Printf("Cleaned %d ghost annotations.\n", cleaned)
}
