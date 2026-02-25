package protocol

import (
	"path/filepath"
	"testing"
)

func TestSocketPathLinuxPrefersXDG(t *testing.T) {
	t.Parallel()

	got := socketPath("linux", 1000, "/tmp/fallback", "/run/user/1000", true)
	want := filepath.Join("/run/user/1000", "portfwd.sock")
	if got != want {
		t.Fatalf("socketPath() = %q, want %q", got, want)
	}
}

func TestSocketPathLinuxIgnoresRelativeXDG(t *testing.T) {
	t.Parallel()

	got := socketPath("linux", 1000, "/tmp/fallback", "relative/runtime", true)
	want := filepath.Join("/run/user", "1000", "portfwd.sock")
	if got != want {
		t.Fatalf("socketPath() = %q, want %q", got, want)
	}
}

func TestSocketPathLinuxFallsBackToTmp(t *testing.T) {
	t.Parallel()

	got := socketPath("linux", 0, "/tmp/fallback", "", false)
	want := filepath.Join("/tmp", "kpf-0", "portfwd.sock")
	if got != want {
		t.Fatalf("socketPath() = %q, want %q", got, want)
	}
}

func TestSocketPathDarwin(t *testing.T) {
	t.Parallel()

	got := socketPath("darwin", 501, "/tmp/fallback", "/run/user/501", true)
	want := filepath.Join("/tmp", "kpf-501", "portfwd.sock")
	if got != want {
		t.Fatalf("socketPath() = %q, want %q", got, want)
	}
}

func TestSocketPathDefaultUsesTempDir(t *testing.T) {
	t.Parallel()

	got := socketPath("windows", 1000, "C:\\Temp", "", false)
	want := filepath.Join("C:\\Temp", "kpf-1000", "portfwd.sock")
	if got != want {
		t.Fatalf("socketPath() = %q, want %q", got, want)
	}
}

func TestDirectoryExists(t *testing.T) {
	t.Parallel()

	if !directoryExists(".") {
		t.Fatalf("directoryExists(\".\") = false, want true")
	}
	if directoryExists("./definitely-does-not-exist") {
		t.Fatalf("directoryExists(nonexistent) = true, want false")
	}
}
