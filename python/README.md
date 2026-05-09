# Antigravity Doctor — Python Version

Standalone Python implementation of Antigravity Doctor. Use this if you can't run the pre-compiled Go binary.

## Requirements

- Python 3.7+
- No external packages needed (stdlib only)

## Usage

### Windows

Double-click `run_doctor.bat`, or:

```bash
python antigravity_doctor.py
```

### macOS / Linux

```bash
python3 antigravity_doctor.py
```

### Clean ghost annotations

```bash
python antigravity_doctor.py --clean-ghosts
```

## Notes

- Close Antigravity before running
- No reboot required — just reopen Antigravity after the fix
- Backup is saved automatically to `antigravity_backup/` next to the script

For more details, see the [main README](../README.md).
