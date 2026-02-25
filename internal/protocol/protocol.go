package protocol

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"time"
)

const (
	// DaemonArg switches kpf into daemon/server mode.
	DaemonArg = "--kpf-daemon"
	// RequestTimeout bounds request/response operations over the daemon socket.
	RequestTimeout = 30 * time.Second
	// SocketDialTimeout bounds individual attempts to dial the daemon socket.
	SocketDialTimeout = 1 * time.Second
	// DefaultSessionTTL is used when a request does not provide a session TTL.
	DefaultSessionTTL = 30 * time.Minute
)

// Request is the daemon RPC payload for ensuring a reusable port-forward
// session and retrieving its local port.
type Request struct {
	// Action indicates the requested daemon operation.
	Action string `json:"action"`
	// Key uniquely identifies a reusable forwarding session configuration.
	Key string `json:"key"`
	// Resource is the kubectl target resource (for example pod/name or svc/name).
	Resource string `json:"resource"`
	// RemotePort is the destination port or named port on the target resource.
	RemotePort string `json:"remote_port"`
	// LocalPort optionally fixes the local bind port. Nil means choose any port.
	LocalPort *int `json:"local_port,omitempty"`
	// Args are normalized kubectl port-forward arguments (without the subcommand).
	Args []string `json:"args"`
	// SessionTTLNanos overrides the idle session TTL for this request.
	SessionTTLNanos int64 `json:"session_ttl_nanos,omitempty"`
}

// Response is the daemon RPC response.
type Response struct {
	// OK indicates whether the request completed successfully.
	OK bool `json:"ok"`
	// LocalPort is the resolved local bind port when OK is true.
	LocalPort int `json:"local_port,omitempty"`
	// Error contains a human-readable error when OK is false.
	Error string `json:"error,omitempty"`
}

// SocketPath returns the OS-specific daemon unix socket path.
func SocketPath() string {
	uid := os.Getuid()
	runUserDir := filepath.Join("/run/user", strconv.Itoa(uid))
	return socketPath(runtime.GOOS, uid, os.TempDir(), os.Getenv("XDG_RUNTIME_DIR"), directoryExists(runUserDir))
}

func socketPath(goos string, uid int, tempDir, xdgRuntimeDir string, runUserDirExists bool) string {
	switch goos {
	case "linux":
		if xdgRuntimeDir != "" && filepath.IsAbs(xdgRuntimeDir) {
			return filepath.Join(xdgRuntimeDir, "portfwd.sock")
		}
		if runUserDirExists {
			return filepath.Join("/run/user", strconv.Itoa(uid), "portfwd.sock")
		}
		return filepath.Join("/tmp", fmt.Sprintf("kpf-%d", uid), "portfwd.sock")
	case "darwin":
		// Keep this path short to avoid macOS unix socket path length limits.
		return filepath.Join("/tmp", fmt.Sprintf("kpf-%d", uid), "portfwd.sock")
	default:
		return filepath.Join(tempDir, fmt.Sprintf("kpf-%d", uid), "portfwd.sock")
	}
}

func directoryExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}
