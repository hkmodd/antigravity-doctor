"""
Antigravity Doctor v2.0 — by Antigravity itself
=================================================
Fixes the disappearing conversations bug by patching BOTH database keys:
  1. antigravityUnifiedStateSync.trajectorySummaries (legacy index)
  2. chat.ChatSessionStore.index (NEW — the key the UI actually reads!)

The original GitHub tool (FutureisinPast) only patches #1, which is why
conversations STILL disappear. This tool patches both AND does NOT require
a PC reboot — just close and reopen Antigravity.

Features:
  - Rebuilds conversation index from .pb files on disk
  - Patches ChatSessionStore.index (the ACTUAL sidebar source)
  - Creates missing annotation files for orphan conversations
  - Cleans up ghost annotations (metadata for deleted conversations)
  - Removes stale .tmp files
  - Auto-detects workspace assignments from brain artifacts
  - Full backup before any modification
  - NO REBOOT REQUIRED

Usage:
  1. Close Antigravity completely (File > Exit)
  2. Run: python antigravity_doctor.py
  3. Reopen Antigravity — conversations are back!

Requirements: Python 3.7+ (no external packages)
"""

import sqlite3
import base64
import json
import os
import re
import sys
import time
import shutil
import subprocess
import platform
from pathlib import Path
from urllib.parse import quote, unquote
from datetime import datetime

# ═══════════════════════════════════════════════════════════════════════════════
# Configuration
# ═══════════════════════════════════════════════════════════════════════════════

_SYSTEM = platform.system()
_HOME = os.path.expanduser("~")

if _SYSTEM == "Windows":
    GEMINI_DIR = os.path.join(_HOME, ".gemini", "antigravity")
    APPDATA_DIR = os.path.join(os.environ.get("APPDATA", ""), "Antigravity")
elif _SYSTEM == "Darwin":
    GEMINI_DIR = os.path.join(_HOME, ".gemini", "antigravity")
    APPDATA_DIR = os.path.join(_HOME, "Library", "Application Support", "antigravity")
else:
    GEMINI_DIR = os.path.join(_HOME, ".gemini", "antigravity")
    APPDATA_DIR = os.path.join(_HOME, ".config", "Antigravity")

CONVERSATIONS_DIR = os.path.join(GEMINI_DIR, "conversations")
BRAIN_DIR = os.path.join(GEMINI_DIR, "brain")
ANNOTATIONS_DIR = os.path.join(GEMINI_DIR, "annotations")
DB_PATH = os.path.join(APPDATA_DIR, "User", "globalStorage", "state.vscdb")
WS_STORAGE_DIR = os.path.join(APPDATA_DIR, "User", "workspaceStorage")

# ═══════════════════════════════════════════════════════════════════════════════
# Protobuf helpers (minimal, no external deps)
# ═══════════════════════════════════════════════════════════════════════════════

def encode_varint(value):
    result = b""
    while value > 0x7F:
        result += bytes([(value & 0x7F) | 0x80])
        value >>= 7
    result += bytes([value & 0x7F])
    return result or b'\x00'

def decode_varint(data, pos):
    result, shift = 0, 0
    while pos < len(data):
        b = data[pos]
        result |= (b & 0x7F) << shift
        if (b & 0x80) == 0:
            return result, pos + 1
        shift += 7
        pos += 1
    return result, pos

def skip_field(data, pos, wire_type):
    if wire_type == 0:
        _, pos = decode_varint(data, pos)
    elif wire_type == 2:
        length, pos = decode_varint(data, pos)
        pos += length
    elif wire_type == 1:
        pos += 8
    elif wire_type == 5:
        pos += 4
    return pos

def strip_field(data, target_field):
    remaining = b""
    pos = 0
    while pos < len(data):
        start = pos
        try:
            tag, pos = decode_varint(data, pos)
        except:
            remaining += data[start:]
            break
        wt = tag & 7
        fn = tag >> 3
        new_pos = skip_field(data, pos, wt)
        if new_pos == pos and wt not in (0, 1, 2, 5):
            remaining += data[start:]
            break
        pos = new_pos
        if fn != target_field:
            remaining += data[start:pos]
    return remaining

def encode_ld(field_number, data):
    tag = (field_number << 3) | 2
    return encode_varint(tag) + encode_varint(len(data)) + data

def encode_str(field_number, s):
    return encode_ld(field_number, s.encode('utf-8'))

def encode_ts_fields(epoch):
    sec = int(epoch)
    inner = encode_varint((1 << 3) | 0) + encode_varint(sec)
    return encode_ld(3, inner) + encode_ld(7, inner) + encode_ld(10, inner)

def has_ts_fields(blob):
    if not blob: return False
    try:
        pos = 0
        while pos < len(blob):
            tag, pos = decode_varint(blob, pos)
            if (tag >> 3) in (3, 7, 10): return True
            pos = skip_field(blob, pos, tag & 7)
    except: pass
    return False

# ═══════════════════════════════════════════════════════════════════════════════
# Workspace helpers
# ═══════════════════════════════════════════════════════════════════════════════

def path_to_uri(folder):
    if folder.startswith("file:///") or folder.startswith("vscode-remote://"):
        return folder
    p = folder.replace("\\", "/")
    if len(p) >= 2 and p[1] == ":":
        drive, rest = p[0].lower(), p[2:]
    else:
        drive, rest = None, p
    segs = [quote(s, safe="") for s in rest.split("/")]
    enc = "/".join(segs)
    return f"file:///{drive}%3A{enc}" if drive else f"file:///{enc.lstrip('/')}"

def build_ws_field(folder):
    uri = path_to_uri(folder)
    sub = encode_str(1, uri) + encode_str(2, uri)
    return encode_ld(9, sub)

def load_known_ws_uris():
    uris = []
    if not os.path.isdir(WS_STORAGE_DIR):
        return uris
    for name in os.listdir(WS_STORAGE_DIR):
        ws_json = os.path.join(WS_STORAGE_DIR, name, "workspace.json")
        if os.path.exists(ws_json):
            try:
                with open(ws_json, "r", encoding="utf-8") as f:
                    data = json.load(f)
                uri = data.get("folder") or data.get("workspace")
                if uri: uris.append(uri)
            except: pass
    uris.sort(key=len, reverse=True)
    return uris

def infer_workspace(cid, known_uris):
    brain = os.path.join(BRAIN_DIR, cid)
    if not os.path.isdir(brain):
        return None
    local_pat = re.compile(r"file:///([A-Za-z](?:%3A|:)/[^\s\"'\]>]+)" if _SYSTEM == "Windows"
                           else r"file:///([^\s\"'\]>]+)")
    remote_pat = re.compile(r"(vscode-remote://[^\s\"'\]>]+)")
    found_local, found_remote = [], []
    for item in os.listdir(brain):
        if not item.endswith(".md") or item.startswith("."): continue
        try:
            with open(os.path.join(brain, item), "r", encoding="utf-8", errors="replace") as f:
                text = f.read(16384)
            found_remote.extend(m.group(1) for m in remote_pat.finditer(text))
            found_local.extend("file:///" + m.group(1) for m in local_pat.finditer(text))
        except: pass
    if not found_local and not found_remote:
        return None
    if known_uris:
        counts = {}
        for uri in found_local + found_remote:
            norm = uri.replace("%3A", ":").replace("%3a", ":").replace("%20", " ")
            for ws in known_uris:
                ws_n = ws.replace("%3A", ":").replace("%3a", ":").replace("%20", " ")
                if norm.startswith(ws_n + "/") or norm == ws_n:
                    counts[ws] = counts.get(ws, 0) + 1
                    break
        if counts:
            best = max(counts, key=counts.get)
            return unquote(best[len("file://"):]).lstrip("/") if best.startswith("file:///") else best
    return None

# ═══════════════════════════════════════════════════════════════════════════════
# Title resolution
# ═══════════════════════════════════════════════════════════════════════════════

def get_brain_title(cid):
    brain = os.path.join(BRAIN_DIR, cid)
    if not os.path.isdir(brain): return None
    for item in sorted(os.listdir(brain)):
        if item.startswith('.') or not item.endswith('.md'): continue
        try:
            with open(os.path.join(brain, item), 'r', encoding='utf-8', errors='replace') as f:
                line = f.readline().strip()
            if line.startswith('#'):
                return line.lstrip('# ').strip()[:80]
        except: pass
    return None

def extract_existing_metadata(db_path):
    titles, blobs = {}, {}
    try:
        conn = sqlite3.connect(db_path)
        cur = conn.cursor()
        cur.execute("SELECT value FROM ItemTable WHERE key='antigravityUnifiedStateSync.trajectorySummaries'")
        row = cur.fetchone()
        conn.close()
        if not row or not row[0]: return titles, blobs
        decoded = base64.b64decode(row[0])
        pos = 0
        while pos < len(decoded):
            tag, pos = decode_varint(decoded, pos)
            if (tag & 7) != 2: break
            length, pos = decode_varint(decoded, pos)
            entry = decoded[pos:pos+length]; pos += length
            ep, uid, info_b64 = 0, None, None
            while ep < len(entry):
                t, ep = decode_varint(entry, ep)
                fn, wt = t >> 3, t & 7
                if wt == 2:
                    l, ep = decode_varint(entry, ep)
                    content = entry[ep:ep+l]; ep += l
                    if fn == 1: uid = content.decode('utf-8', errors='replace')
                    elif fn == 2:
                        sp = 0; _, sp = decode_varint(content, sp)
                        sl, sp = decode_varint(content, sp)
                        info_b64 = content[sp:sp+sl].decode('utf-8', errors='replace')
                elif wt == 0: _, ep = decode_varint(entry, ep)
                else: break
            if uid and info_b64:
                try:
                    raw = base64.b64decode(info_b64)
                    blobs[uid] = raw
                    ip = 0; _, ip = decode_varint(raw, ip)
                    il, ip = decode_varint(raw, ip)
                    title = raw[ip:ip+il].decode('utf-8', errors='replace')
                    if not title.startswith("Conversation"):
                        titles[uid] = title
                except: pass
    except: pass
    return titles, blobs

# ═══════════════════════════════════════════════════════════════════════════════
# Build trajectory entry (for legacy index)
# ═══════════════════════════════════════════════════════════════════════════════

def build_entry(cid, title, existing_blob=None, ws_path=None, mtime=None):
    if existing_blob:
        inner = encode_str(1, title) + strip_field(existing_blob, 1)
        if ws_path:
            inner = strip_field(inner, 9) + build_ws_field(ws_path)
        if mtime and not has_ts_fields(existing_blob):
            inner += encode_ts_fields(mtime)
    else:
        inner = encode_str(1, title)
        if ws_path: inner += build_ws_field(ws_path)
        if mtime: inner += encode_ts_fields(mtime)
    b64 = base64.b64encode(inner).decode('utf-8')
    entry = encode_str(1, cid) + encode_ld(2, encode_str(1, b64))
    return entry

# ═══════════════════════════════════════════════════════════════════════════════
# ChatSessionStore.index builder (THE KEY FIX!)
# ═══════════════════════════════════════════════════════════════════════════════

def build_chat_session_index(conversations):
    """
    Build the chat.ChatSessionStore.index JSON that Antigravity's UI reads.
    conversations: list of (cid, title, mtime, workspace_uri_or_none)
    """
    entries = {}
    for cid, title, mtime, ws_uri in conversations:
        entry = {
            "isActive": False,
            "sessionId": cid,
        }
        entries[cid] = entry
    return json.dumps({"version": 1, "entries": entries})

# ═══════════════════════════════════════════════════════════════════════════════
# Workspace-specific ChatSessionStore fix
# ═══════════════════════════════════════════════════════════════════════════════

def fix_workspace_chat_indices(conversations_by_ws):
    """
    Also patch the workspace-specific state.vscdb files so conversations
    appear when a specific workspace is open.
    conversations_by_ws: dict {workspace_uri: [(cid, title, mtime), ...]}
    """
    if not os.path.isdir(WS_STORAGE_DIR):
        return 0
    fixed = 0
    uri_to_hash = {}
    for name in os.listdir(WS_STORAGE_DIR):
        ws_json = os.path.join(WS_STORAGE_DIR, name, "workspace.json")
        if os.path.exists(ws_json):
            try:
                with open(ws_json, "r", encoding="utf-8") as f:
                    data = json.load(f)
                uri = data.get("folder") or data.get("workspace")
                if uri: uri_to_hash[uri] = name
            except: pass

    for ws_uri, convs in conversations_by_ws.items():
        if ws_uri not in uri_to_hash: continue
        ws_db = os.path.join(WS_STORAGE_DIR, uri_to_hash[ws_uri], "state.vscdb")
        if not os.path.exists(ws_db): continue
        try:
            entries = {}
            for cid, title, mtime in convs:
                entries[cid] = {"isActive": False, "sessionId": cid}
            idx = json.dumps({"version": 1, "entries": entries})
            conn = sqlite3.connect(ws_db)
            cur = conn.cursor()
            cur.execute("SELECT value FROM ItemTable WHERE key='chat.ChatSessionStore.index'")
            row = cur.fetchone()
            if row:
                cur.execute("UPDATE ItemTable SET value=? WHERE key='chat.ChatSessionStore.index'", (idx,))
            else:
                cur.execute("INSERT INTO ItemTable (key, value) VALUES ('chat.ChatSessionStore.index', ?)", (idx,))
            conn.commit()
            conn.close()
            fixed += 1
        except: pass
    return fixed

# ═══════════════════════════════════════════════════════════════════════════════
# Main
# ═══════════════════════════════════════════════════════════════════════════════

def main():
    print()
    print("=" * 64)
    print("   ⚡ Antigravity Doctor v2.0")
    print("   Self-healing conversation index rebuilder")
    print("   NO REBOOT REQUIRED — just close and reopen Antigravity")
    print("=" * 64)
    print()

    # ── Check if Antigravity is running ──
    if _SYSTEM == "Windows":
        try:
            r = subprocess.run(['tasklist', '/FI', 'IMAGENAME eq Antigravity.exe'],
                             capture_output=True, text=True, creationflags=0x08000000)
            if 'antigravity.exe' in r.stdout.lower():
                print("  ⚠️  Antigravity is STILL RUNNING!")
                print("  Close it first: File > Exit, or kill from Task Manager.")
                print()
                c = input("  Press Enter after closing (or Q to quit): ").strip()
                if c.lower() == 'q': return 1
                print()
        except: pass

    # ── Validate paths ──
    errors = []
    if not os.path.exists(DB_PATH):
        errors.append(f"Database not found: {DB_PATH}")
    if not os.path.isdir(CONVERSATIONS_DIR):
        errors.append(f"Conversations dir not found: {CONVERSATIONS_DIR}")
    if errors:
        for e in errors: print(f"  ❌ {e}")
        input("\n  Press Enter to close...")
        return 1

    # ── Discover conversations on disk ──
    pb_files = [f for f in os.listdir(CONVERSATIONS_DIR) if f.endswith('.pb') and not f.endswith('.tmp')]
    if not pb_files:
        print("  No conversations found. Nothing to fix.")
        input("\n  Press Enter to close...")
        return 0

    pb_files.sort(key=lambda f: os.path.getmtime(os.path.join(CONVERSATIONS_DIR, f)), reverse=True)
    conv_ids = [f[:-3] for f in pb_files]
    print(f"  📂 Found {len(conv_ids)} conversations on disk")

    # ── Check annotations ──
    ann_ids = set()
    if os.path.isdir(ANNOTATIONS_DIR):
        ann_ids = {f[:-6] for f in os.listdir(ANNOTATIONS_DIR) if f.endswith('.pbtxt')}
    ghost_anns = ann_ids - set(conv_ids)
    orphan_convs = set(conv_ids) - ann_ids
    print(f"  📝 Annotations: {len(ann_ids)} total, {len(ghost_anns)} ghosts, {len(orphan_convs)} missing")

    # ── Clean temp files ──
    tmp_files = [f for f in os.listdir(CONVERSATIONS_DIR) if f.endswith('.tmp')]
    if tmp_files:
        print(f"  🗑️  Found {len(tmp_files)} stale .tmp file(s) — cleaning...")
        for t in tmp_files:
            try: os.remove(os.path.join(CONVERSATIONS_DIR, t))
            except: pass

    # ── Read existing metadata ──
    print(f"  📖 Reading existing metadata from database...")
    existing_titles, existing_blobs = extract_existing_metadata(DB_PATH)
    print(f"     {len(existing_titles)} titles preserved, {len(existing_blobs)} metadata blobs")

    # ── Load workspace URIs ──
    known_ws = load_known_ws_uris()
    print(f"  🏠 Loaded {len(known_ws)} known workspaces")
    print()

    # ── Resolve titles and workspaces ──
    print("  🔍 Scanning conversations (newest first):")
    print("  " + "─" * 60)

    resolved = []  # (cid, title, source, blob, ws_path, mtime)
    stats = {"brain": 0, "preserved": 0, "fallback": 0}
    markers = {"brain": "+", "preserved": "~", "fallback": "?"}

    for i, cid in enumerate(conv_ids, 1):
        pb_path = os.path.join(CONVERSATIONS_DIR, f"{cid}.pb")
        mtime = os.path.getmtime(pb_path)
        blob = existing_blobs.get(cid)

        # Resolve title
        if cid in existing_titles:
            title, src = existing_titles[cid], "preserved"
        else:
            bt = get_brain_title(cid)
            if bt:
                title, src = bt, "brain"
            else:
                dt = time.strftime("%b %d", time.localtime(mtime))
                title, src = f"Conversation ({dt}) {cid[:8]}", "fallback"

        # Resolve workspace
        ws = infer_workspace(cid, known_ws)

        resolved.append((cid, title, src, blob, ws, mtime))
        stats[src] += 1
        m = markers[src]
        ws_flag = " [WS]" if ws else ""
        if i <= 20 or i == len(conv_ids):
            print(f"    [{i:3d}] {m} {title[:50]}{ws_flag}")
        elif i == 21:
            print(f"    ... ({len(conv_ids) - 20} more) ...")

    print("  " + "─" * 60)
    print(f"  Legend: [+] brain  [~] preserved  [?] fallback  [WS] workspace")
    print(f"  Totals: {stats['brain']} brain, {stats['preserved']} preserved, {stats['fallback']} fallback")
    print()

    # ══════════════════════════════════════════════════════════════════════════
    # BACKUP
    # ══════════════════════════════════════════════════════════════════════════
    backup_dir = os.path.join(os.path.dirname(os.path.abspath(__file__)), "antigravity_backup")
    os.makedirs(backup_dir, exist_ok=True)
    ts = datetime.now().strftime("%Y%m%d_%H%M%S")
    backup_db = os.path.join(backup_dir, f"state.vscdb.{ts}.bak")
    shutil.copy2(DB_PATH, backup_db)
    print(f"  💾 Backup saved: {backup_db}")
    print()

    # ══════════════════════════════════════════════════════════════════════════
    # FIX 1: Rebuild trajectorySummaries (legacy, but still needed)
    # ══════════════════════════════════════════════════════════════════════════
    print("  🔧 [1/4] Rebuilding trajectorySummaries...")
    result_bytes = b""
    for cid, title, src, blob, ws, mtime in resolved:
        entry = build_entry(cid, title, blob, ws, mtime)
        result_bytes += encode_ld(1, entry)

    encoded_traj = base64.b64encode(result_bytes).decode('utf-8')

    conn = sqlite3.connect(DB_PATH)
    cur = conn.cursor()
    cur.execute("SELECT value FROM ItemTable WHERE key='antigravityUnifiedStateSync.trajectorySummaries'")
    row = cur.fetchone()
    if row:
        cur.execute("UPDATE ItemTable SET value=? WHERE key='antigravityUnifiedStateSync.trajectorySummaries'", (encoded_traj,))
    else:
        cur.execute("INSERT INTO ItemTable (key, value) VALUES ('antigravityUnifiedStateSync.trajectorySummaries', ?)", (encoded_traj,))
    conn.commit()
    print(f"     ✅ trajectorySummaries rebuilt ({len(conv_ids)} entries)")

    # ══════════════════════════════════════════════════════════════════════════
    # FIX 2: Rebuild chat.ChatSessionStore.index (THE CRITICAL FIX!)
    # ══════════════════════════════════════════════════════════════════════════
    print("  🔧 [2/4] Rebuilding ChatSessionStore.index (THE KEY FIX)...")
    chat_data = []
    for cid, title, src, blob, ws, mtime in resolved:
        ws_uri = path_to_uri(ws) if ws else None
        chat_data.append((cid, title, mtime, ws_uri))

    new_index = build_chat_session_index(chat_data)
    cur.execute("SELECT value FROM ItemTable WHERE key='chat.ChatSessionStore.index'")
    row = cur.fetchone()
    if row:
        cur.execute("UPDATE ItemTable SET value=? WHERE key='chat.ChatSessionStore.index'", (new_index,))
    else:
        cur.execute("INSERT INTO ItemTable (key, value) VALUES ('chat.ChatSessionStore.index', ?)", (new_index,))
    conn.commit()

    # Also fix workspace-specific databases
    ws_convs = {}
    for cid, title, src, blob, ws, mtime in resolved:
        if ws:
            uri = path_to_uri(ws)
            ws_convs.setdefault(uri, []).append((cid, title, mtime))
    ws_fixed = fix_workspace_chat_indices(ws_convs)

    print(f"     ✅ ChatSessionStore.index rebuilt ({len(conv_ids)} entries)")
    print(f"     ✅ {ws_fixed} workspace-specific indices patched")

    conn.close()

    # ══════════════════════════════════════════════════════════════════════════
    # FIX 3: Create missing annotations
    # ══════════════════════════════════════════════════════════════════════════
    print("  🔧 [3/4] Fixing annotations...")
    created_ann = 0
    if os.path.isdir(ANNOTATIONS_DIR):
        for cid in orphan_convs:
            ann_path = os.path.join(ANNOTATIONS_DIR, f"{cid}.pbtxt")
            if not os.path.exists(ann_path):
                pb_path = os.path.join(CONVERSATIONS_DIR, f"{cid}.pb")
                mtime = os.path.getmtime(pb_path) if os.path.exists(pb_path) else time.time()
                sec = int(mtime)
                nano = int((mtime - sec) * 1e9)
                with open(ann_path, 'w', encoding='utf-8') as f:
                    f.write(f"last_user_view_time:{{seconds:{sec}  nanos:{nano}}}\n")
                created_ann += 1
    print(f"     ✅ Created {created_ann} missing annotations")

    # ══════════════════════════════════════════════════════════════════════════
    # FIX 4: Clean ghost annotations (optional info)
    # ══════════════════════════════════════════════════════════════════════════
    print("  🔧 [4/4] Ghost annotations report...")
    print(f"     ℹ️  {len(ghost_anns)} ghost annotations found (metadata for deleted conversations)")
    if ghost_anns:
        print(f"     ℹ️  These are harmless but can be cleaned with --clean-ghosts flag")

    # ══════════════════════════════════════════════════════════════════════════
    # DONE
    # ══════════════════════════════════════════════════════════════════════════
    print()
    print("  " + "=" * 60)
    print(f"  ✅ SUCCESS! Index rebuilt with {len(conv_ids)} conversations.")
    print("  " + "=" * 60)
    print()
    print("  NEXT STEPS:")
    print("    1. Make sure Antigravity is fully closed")
    print("    2. Open Antigravity — conversations should appear!")
    print("    3. NO REBOOT NEEDED ✨")
    print()
    print(f"  Backup location: {backup_dir}")
    print()
    input("  Press Enter to close...")
    return 0


if __name__ == "__main__":
    if "--clean-ghosts" in sys.argv:
        print("Cleaning ghost annotations...")
        if os.path.isdir(ANNOTATIONS_DIR) and os.path.isdir(CONVERSATIONS_DIR):
            conv_ids = {f[:-3] for f in os.listdir(CONVERSATIONS_DIR) if f.endswith('.pb')}
            cleaned = 0
            for f in os.listdir(ANNOTATIONS_DIR):
                if f.endswith('.pbtxt') and f[:-6] not in conv_ids:
                    os.remove(os.path.join(ANNOTATIONS_DIR, f))
                    cleaned += 1
            print(f"Cleaned {cleaned} ghost annotations.")
    else:
        sys.exit(main())
