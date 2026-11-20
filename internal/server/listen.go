package server

import (
	"context"
	"fmt"
	"hash/crc32"
	"net"
	"os"
	"os/exec"
	"strconv"
	"time"

	gopsnet "github.com/shirou/gopsutil/v4/net"
	"golang.org/x/sys/windows"

	"lango/internal/applog"
)

// Port computes the deterministic port for an application name.
func Port(appName string) int {
	return 20000 + int(crc32.ChecksumIEEE([]byte(appName))%25000)
}

// PortKiller finds and terminates listeners occupying a TCP port.
type PortKiller interface {
	FindListener(port int) (pid int, err error)
	Kill(pid int) error
}

// RealPortKiller implements PortKiller on Windows.
type RealPortKiller struct{}

// FindListener finds the PID listening on 127.0.0.1:port or 0.0.0.0:port.
func (k *RealPortKiller) FindListener(port int) (int, error) {
	conns, err := gopsnet.Connections("tcp")
	if err != nil {
		return 0, err
	}
	for _, c := range conns {
		if c.Status == "LISTEN" && int(c.Laddr.Port) == port {
			if c.Laddr.IP == "127.0.0.1" || c.Laddr.IP == "0.0.0.0" {
				if c.Pid > 0 {
					return int(c.Pid), nil
				}
			}
		}
	}
	return 0, nil
}

// Kill terminates the process with taskkill /F /PID without /T.
func (k *RealPortKiller) Kill(pid int) error {
	if pid <= 0 || pid == os.Getpid() {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "taskkill", "/F", "/PID", strconv.Itoa(pid))
	cmd.SysProcAttr = &windows.SysProcAttr{
		CreationFlags: windows.CREATE_NO_WINDOW,
		HideWindow:    true,
	}
	return cmd.Run()
}

// Listen attempts to bind 127.0.0.1:port with kill-on-failure and fallbacks.
func Listen(port int, killer PortKiller) (net.Listener, int, error) {
	log := applog.New("lango")

	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		log.Info("Listening on 127.0.0.1:%d", port)
		return ln, port, nil
	}

	if killer == nil {
		killer = &RealPortKiller{}
	}

	pid, findErr := killer.FindListener(port)
	if findErr == nil && pid > 0 && pid != os.Getpid() {
		log.Warn("Freed port %d (killed PID %d)", port, pid)
		_ = killer.Kill(pid)
		for attempt := 0; attempt < 10; attempt++ {
			time.Sleep(100 * time.Millisecond)
			if retryLn, retryErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port)); retryErr == nil {
				log.Info("Listening on 127.0.0.1:%d", port)
				return retryLn, port, nil
			}
		}
	}

	for p := port + 1; p <= port+255; p++ {
		if p < 1024 || p > 65535 {
			continue
		}
		if fallbackLn, fallbackErr := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", p)); fallbackErr == nil {
			log.Info("Listening on 127.0.0.1:%d", p)
			return fallbackLn, p, nil
		}
	}

	lastLn, lastErr := net.Listen("tcp", "127.0.0.1:0")
	if lastErr != nil {
		return nil, 0, lastErr
	}
	actualPort := lastLn.Addr().(*net.TCPAddr).Port
	log.Info("Listening on 127.0.0.1:%d", actualPort)
	return lastLn, actualPort, nil
}
