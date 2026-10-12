// Package selfupdate implements ARTEX's one-click update from the UI: fetch a new binary from a GitHub Release,
// verify it, stage it, and atomically swap it in on the next startup.
//
// Overall division of labor (see start.sh / start.bat):
//
//	startup script = a dumb supervisor loop, only responsible for "after the process exits, decide by exit code whether to relaunch"
//	this package   = all the error-prone logic (download / SHA256 verify / smoke / swap / rollback on failure)
//
// The swap lives in Go rather than the script because SHA256 verification and the smoke test would need two implementations on sh and bat
// (sha256sum / shasum / certutil), and this is exactly the part that must not go wrong — swap in a
// binary that cannot run and the supervisor faithfully relaunches it over and over, leaving the user to rescue it by hand on the machine.
//
// A full upgrade goes through three process starts:
//
//	① the old server receives /api/update/apply → download+verify → stage artex.new → exit 75
//	② the script relaunches the old version → Bootstrap finds artex.new → verify+smoke → swap → exit 75
//	③ the script relaunches, now the new version → Bootstrap counts one attempt → clears the marker after a successful start
//
// Any failed step reverts to the old version: ② if verification fails, delete the staged file and keep running the old version; ③ if it fails 3 times in a row to survive
// until the marker is cleared (crashes on start), automatically swap artex.old back in.
package selfupdate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ExitRestart is the exit code for "please, supervisor, relaunch me" (EX_TEMPFAIL). When the startup script sees it
// it relaunches immediately, not counting against crash backoff. 0 means the user stopped normally (the script exits the loop); anything else is treated as a crash.
const ExitRestart = 75

// maxAttempts is the number of startup attempts allowed after a swap. The new version increments the count on each start, and surviving
// past settleDelay clears the marker; crashing maxAttempts times in a row means the new version simply cannot start, so it auto-rolls back.
const maxAttempts = 3

// Paths are all the files involved in one upgrade, all kept under **the directory of the executable**.
// Deliberately not CWD: when run as a service the working directory may be / or any path, and using CWD would make staged files land
// elsewhere, breaking the swap logic outright.
type Paths struct {
	Dir     string // the directory of the executable
	Current string // the currently running binary       artex      / artex.exe
	New     string // the staged new version             artex.new  / artex.new.exe
	Sum     string // the new version's sha256 (hex)     artex.new.sha256 / artex.new.exe.sha256
	Old     string // the old version backed up before the swap  artex.old  / artex.old.exe
	Marker  string // the upgrade status marker          artex.upgrade.json
}

// ResolvePaths derives all upgrade paths from the current executable.
//
// On Windows the .new/.old must also carry the .exe suffix, or the smoke test and post-swap execution both fail,
// so the suffix is stripped first and then reassembled, making the naming symmetric across both platforms.
func ResolvePaths() (Paths, error) {
	exe, err := os.Executable()
	if err != nil {
		return Paths{}, fmt.Errorf("locate the executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Dir(exe)
	name := filepath.Base(exe)
	ext := filepath.Ext(name) // ".exe" on Windows, usually empty on Unix
	stem := strings.TrimSuffix(name, ext)

	join := func(suffix string) string { return filepath.Join(dir, stem+suffix+ext) }
	return Paths{
		Dir:     dir,
		Current: exe,
		New:     join(".new"),
		Sum:     join(".new") + ".sha256",
		Old:     join(".old"),
		Marker:  filepath.Join(dir, stem+".upgrade.json"),
	}, nil
}

// marker records the progress of one swap, used to trigger an automatic rollback when the new version cannot start.
type marker struct {
	From     string `json:"from"`     // the version before the upgrade
	To       string `json:"to"`       // the target version
	Attempts int    `json:"attempts"` // the number of startup attempts after the swap
	StagedAt int64  `json:"staged_at"`
}

func readMarker(path string) (marker, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return marker{}, false
	}
	var m marker
	if json.Unmarshal(b, &m) != nil {
		return marker{}, false
	}
	return m, true
}

func writeMarker(path string, m marker) error {
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

// cleanStaged removes the staged files. A successful swap, a failed verification, and a user cancellation all go through it, to avoid a leftover
// artex.new being retried on the next startup.
func cleanStaged(p Paths) {
	_ = os.Remove(p.New)
	_ = os.Remove(p.Sum)
}

// CompareVersions compares two version numbers and returns -1/0/1 (a<b / a==b / a>b).
// ok=false means at least one side is not a comparable version number (for example a local dev build's "dev" or
// a git describe output like "0.3.7-2-gabc1234-dirty"), in which case the caller should disable one-click updates,
// or it would "upgrade" a dev build to a stable release and overwrite uncommitted changes.
func CompareVersions(a, b string) (int, bool) {
	av, aok := parseVersion(a)
	bv, bok := parseVersion(b)
	if !aok || !bok {
		return 0, false
	}
	for i := range 3 {
		if av[i] != bv[i] {
			if av[i] < bv[i] {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

// parseVersion parses a version number of the form "v0.3.7" / "0.3.7" into [3]int.
//
// It accepts only a clean three-part form: on a non-tag build build.sh uses git describe to produce
// a suffixed version like "0.3.7-2-gabc1234", which must be judged incomparable rather than treated as
// 0.3.7 — otherwise a dev build would be mistaken for "already latest" or overwritten by a stable release.
func parseVersion(s string) ([3]int, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return [3]int{}, false
	}
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return [3]int{}, false
	}
	var out [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return [3]int{}, false
		}
		out[i] = n
	}
	return out, true
}

// InDocker reports whether the process runs inside a container. Under Docker a swap writes to the container's writable layer,
// and `docker compose up -d` rebuilding the container reverts to the image's bundled version — this is expected behavior
// (the user is pulling a new image anyway), but the frontend needs to be able to explain it clearly.
func InDocker() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	b, err := os.ReadFile("/proc/1/cgroup")
	if err != nil {
		return false
	}
	s := string(b)
	return strings.Contains(s, "docker") || strings.Contains(s, "containerd")
}
