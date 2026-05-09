package main

import (
	"bufio"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

// === Title Resolution ===

func getBrainTitle(cid, brainDir string) string {
	dir := filepath.Join(brainDir, cid)
	files, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, f := range files {
		name := f.Name()
		if strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".md") {
			continue
		}
		fh, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		scanner := bufio.NewScanner(fh)
		scanner.Scan()
		line := strings.TrimSpace(scanner.Text())
		fh.Close()
		if strings.HasPrefix(line, "#") {
			title := strings.TrimLeft(line, "# ")
			if len(title) > 80 {
				title = title[:80]
			}
			return title
		}
	}
	return ""
}

func resolveTitle(cid string, existing map[string]string, brainDir string, mtime float64) (string, string) {
	if t, ok := existing[cid]; ok {
		return t, "preserved"
	}
	if t := getBrainTitle(cid, brainDir); t != "" {
		return t, "brain"
	}
	dt := time.Unix(int64(mtime), 0).Format("Jan 02")
	return "Conversation (" + dt + ") " + cid[:8], "fallback"
}

// === Workspace Resolution ===

func pathToURI(folder string) string {
	if strings.HasPrefix(folder, "file:///") || strings.HasPrefix(folder, "vscode-remote://") {
		return folder
	}
	p := strings.ReplaceAll(folder, "\\", "/")
	if len(p) >= 2 && p[1] == ':' {
		drive := strings.ToLower(string(p[0]))
		rest := p[2:]
		segs := strings.Split(rest, "/")
		var encoded []string
		for _, s := range segs {
			encoded = append(encoded, url.PathEscape(s))
		}
		return "file:///" + drive + "%3A" + strings.Join(encoded, "/")
	}
	return "file:///" + strings.TrimLeft(p, "/")
}

func buildWorkspaceField(folder string) []byte {
	uri := pathToURI(folder)
	sub := encodeStringField(1, uri)
	sub = append(sub, encodeStringField(2, uri)...)
	return encodeLengthDelimited(9, sub)
}

func loadWorkspaceURIs(wsDir string) []string {
	if wsDir == "" {
		return nil
	}
	dirs, err := os.ReadDir(wsDir)
	if err != nil {
		return nil
	}
	var uris []string
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
				uris = append(uris, uri)
			} else if uri, ok := ws["workspace"].(string); ok && uri != "" {
				uris = append(uris, uri)
			}
		}
	}
	// Sort longest first for prefix matching
	sort.Slice(uris, func(i, j int) bool { return len(uris[i]) > len(uris[j]) })
	return uris
}

func inferWorkspace(cid, brainDir string, knownURIs []string) string {
	dir := filepath.Join(brainDir, cid)
	if _, err := os.Stat(dir); err != nil {
		return ""
	}

	var localPat, remotePat *regexp.Regexp
	if runtime.GOOS == "windows" {
		localPat = regexp.MustCompile(`file:///([A-Za-z](?:%3A|:)/[^\s"'\]>]+)`)
	} else {
		localPat = regexp.MustCompile(`file:///([^\s"'\]>]+)`)
	}
	remotePat = regexp.MustCompile(`(vscode-remote://[^\s"'\]>]+)`)

	var foundLocal, foundRemote []string
	files, _ := os.ReadDir(dir)
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".md") || strings.HasPrefix(f.Name(), ".") {
			continue
		}
		fh, err := os.Open(filepath.Join(dir, f.Name()))
		if err != nil {
			continue
		}
		limited, _ := io.ReadAll(io.LimitReader(fh, 16384))
		fh.Close()
		text := string(limited)
		for _, m := range localPat.FindAllStringSubmatch(text, -1) {
			foundLocal = append(foundLocal, "file:///"+m[1])
		}
		for _, m := range remotePat.FindAllStringSubmatch(text, -1) {
			foundRemote = append(foundRemote, m[1])
		}
	}

	if len(foundLocal) == 0 && len(foundRemote) == 0 {
		return ""
	}

	if len(knownURIs) > 0 {
		counts := map[string]int{}
		normalize := func(s string) string {
			s = strings.ReplaceAll(s, "%3A", ":")
			s = strings.ReplaceAll(s, "%3a", ":")
			s = strings.ReplaceAll(s, "%20", " ")
			return s
		}
		for _, uri := range append(foundLocal, foundRemote...) {
			norm := normalize(uri)
			for _, ws := range knownURIs {
				wsNorm := normalize(ws)
				if strings.HasPrefix(norm, wsNorm+"/") || norm == wsNorm {
					counts[ws]++
					break
				}
			}
		}
		if len(counts) > 0 {
			var best string
			var bestCount int
			for k, v := range counts {
				if v > bestCount {
					best = k
					bestCount = v
				}
			}
			if strings.HasPrefix(best, "file:///") {
				decoded, _ := url.PathUnescape(best[len("file://"):])
				if runtime.GOOS == "windows" && len(decoded) >= 3 && decoded[0] == '/' && decoded[2] == ':' {
					decoded = decoded[1:]
				}
				return decoded
			}
			return best
		}
	}
	return ""
}

// === Metadata Extraction ===

func extractExistingMetadata(dbPath string) (map[string]string, map[string][]byte) {
	titles := map[string]string{}
	blobs := map[string][]byte{}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return titles, blobs
	}
	defer db.Close()

	var raw string
	err = db.QueryRow("SELECT value FROM ItemTable WHERE key='antigravityUnifiedStateSync.trajectorySummaries'").Scan(&raw)
	if err != nil || raw == "" {
		return titles, blobs
	}

	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return titles, blobs
	}

	pos := 0
	for pos < len(decoded) {
		tag, newPos := decodeVarint(decoded, pos)
		if newPos == pos || (tag&7) != 2 {
			break
		}
		pos = newPos
		length, newPos := decodeVarint(decoded, pos)
		pos = newPos
		if pos+int(length) > len(decoded) {
			break
		}
		entry := decoded[pos : pos+int(length)]
		pos += int(length)

		// Parse entry for UUID (field 1) and info blob (field 2)
		ep := 0
		var uid, infoB64 string
		for ep < len(entry) {
			t, np := decodeVarint(entry, ep)
			if np == ep {
				break
			}
			ep = np
			fn, wt := t>>3, t&7
			if wt == 2 {
				l, np := decodeVarint(entry, ep)
				ep = np
				if ep+int(l) > len(entry) {
					break
				}
				content := entry[ep : ep+int(l)]
				ep += int(l)
				if fn == 1 {
					uid = string(content)
				} else if fn == 2 {
					sp := 0
					_, sp = decodeVarint(content, sp)
					sl, sp := decodeVarint(content, sp)
					if sp+int(sl) <= len(content) {
						infoB64 = string(content[sp : sp+int(sl)])
					}
				}
			} else if wt == 0 {
				_, ep = decodeVarint(entry, ep)
			} else {
				break
			}
		}

		if uid != "" && infoB64 != "" {
			rawInner, err := base64.StdEncoding.DecodeString(infoB64)
			if err != nil {
				continue
			}
			blobs[uid] = rawInner

			// Extract title from field 1
			ip := 0
			_, ip = decodeVarint(rawInner, ip)
			il, ip := decodeVarint(rawInner, ip)
			if ip+int(il) <= len(rawInner) {
				title := string(rawInner[ip : ip+int(il)])
				if !strings.HasPrefix(title, "Conversation") {
					titles[uid] = title
				}
			}
		}
	}
	return titles, blobs
}

// === Trajectory Entry Builder ===

func buildTrajectoryEntry(cid, title string, existingBlob []byte, wsPath string, mtime float64) []byte {
	var inner []byte
	if len(existingBlob) > 0 {
		inner = append(encodeStringField(1, title), stripField(existingBlob, 1)...)
		if wsPath != "" {
			inner = append(stripField(inner, 9), buildWorkspaceField(wsPath)...)
		}
		if mtime > 0 && !hasTimestampFields(existingBlob) {
			inner = append(inner, encodeTimestampFields(mtime)...)
		}
	} else {
		inner = encodeStringField(1, title)
		if wsPath != "" {
			inner = append(inner, buildWorkspaceField(wsPath)...)
		}
		if mtime > 0 {
			inner = append(inner, encodeTimestampFields(mtime)...)
		}
	}

	b64 := base64.StdEncoding.EncodeToString(inner)
	entry := encodeStringField(1, cid)
	entry = append(entry, encodeLengthDelimited(2, encodeStringField(1, b64))...)
	return entry
}
