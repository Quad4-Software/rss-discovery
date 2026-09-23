package sandbox

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/landlock-lsm/go-landlock/landlock"
)

// Apply restricts filesystem and network access using Landlock V10 BestEffort.
// Call after opening DuckDB and creating data dirs, before serving traffic.
// listenAddrs is every address the process will bind (server, metrics).
func Apply(dataDir, dbPath, seedDir string, listenAddrs []string, enable bool) error {
	if !enable {
		slog.Info("landlock disabled")
		return nil
	}

	absData, err := filepath.Abs(dataDir)
	if err != nil {
		return err
	}
	absDB, err := filepath.Abs(filepath.Dir(dbPath))
	if err != nil {
		return err
	}
	absSeed, err := filepath.Abs(seedDir)
	if err != nil {
		return err
	}
	for _, d := range []string{absData, absDB, absSeed} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}

	ports := map[uint16]struct{}{}
	for _, addr := range listenAddrs {
		if addr == "" {
			continue
		}
		p, err := listenPort(addr)
		if err != nil {
			return err
		}
		ports[p] = struct{}{}
	}

	ro := []string{"/usr", "/lib", "/lib64", "/bin", "/sbin", "/etc/ssl", "/etc/ca-certificates", "/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf", "/etc/passwd"}
	roDirs, roFiles := splitDirsFiles(existingOnly(ro))
	if len(roDirs) == 0 && len(roFiles) == 0 {
		roDirs = []string{"/usr"}
	}

	rules := []landlock.Rule{
		landlock.RWDirs(absData, absDB),
		landlock.RODirs(absSeed),
		landlock.ConnectTCP(443),
		landlock.ConnectTCP(80),
		landlock.ConnectTCP(53),
		landlock.BindUDP(0),
		landlock.ConnectSendUDP(53),
	}
	for p := range ports {
		rules = append(rules, landlock.BindTCP(p))
	}
	if len(roDirs) > 0 {
		rules = append(rules, landlock.RODirs(roDirs...))
	}
	if len(roFiles) > 0 {
		rules = append(rules, landlock.ROFiles(roFiles...))
	}

	tmp := os.TempDir()
	if tmp != "" {
		rules = append(rules, landlock.RWDirs(tmp))
	}

	err = landlock.V10.BestEffort().Restrict(rules...)
	if err != nil {
		return fmt.Errorf("landlock: %w", err)
	}
	slog.Info("landlock applied", "abi", "V10", "data", absData, "listen_ports", len(ports))
	return nil
}

func listenPort(addr string) (uint16, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		if strings.HasPrefix(addr, ":") {
			portStr = strings.TrimPrefix(addr, ":")
		} else {
			return 0, fmt.Errorf("listen addr: %w", err)
		}
	}
	_ = host
	n, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return 0, fmt.Errorf("listen port: %w", err)
	}
	return uint16(n), nil
}

func existingOnly(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			out = append(out, p)
		}
	}
	return out
}

// splitDirsFiles partitions paths into directories and regular files.
// landlock_add_rule rejects directory access rights on non-directories.
// Symlinks are resolved and the target classified too: Landlock checks
// the resolved inode, so ruling only the link would still deny access.
func splitDirsFiles(paths []string) (dirs, files []string) {
	seen := map[string]bool{}
	add := func(p string) {
		if seen[p] {
			return
		}
		seen[p] = true
		info, err := os.Stat(p)
		if err != nil {
			return
		}
		if info.IsDir() {
			dirs = append(dirs, p)
		} else {
			files = append(files, p)
		}
	}
	for _, p := range paths {
		add(p)
		if real, err := filepath.EvalSymlinks(p); err == nil {
			add(real)
		}
	}
	return dirs, files
}
