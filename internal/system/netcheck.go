package system

import (
	"bufio"
	"context"
	"net"
	"strconv"
	"strings"
	"time"
)

// Step is one line of a connection test result.
type Step struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

// TestConnection checks, step by step, that a device can be reached over SSH:
// the name resolves, the SSH port accepts connections, and an SSH server answers.
// It stops at the first failing step and says what to try.
func TestConnection(ctx context.Context, host string, port int) []Step {
	var steps []Step
	fail := func(name, detail string) []Step { return append(steps, Step{name, false, detail}) }
	pass := func(name, detail string) { steps = append(steps, Step{name, true, detail}) }

	if port <= 0 {
		port = 22
	}
	host = strings.TrimSpace(host)
	if host == "" {
		return fail("Find the device on your network", "No device name or IP address entered.")
	}

	if ip := net.ParseIP(host); ip != nil {
		pass("Find the device on your network", "Using IP address "+ip.String())
	} else {
		rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := net.DefaultResolver.LookupHost(rctx, host)
		cancel()
		if err != nil || len(addrs) == 0 {
			return fail("Find the device on your network",
				"Windows could not find \""+host+"\". Check the name, or use the device's IP address instead (see your router's device list).")
		}
		pass("Find the device on your network", host+" is "+addrs[0])
	}

	d := net.Dialer{Timeout: 5 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return fail("Connect to the SSH port ("+strconv.Itoa(port)+")",
			"Nothing answered. Make sure the device is awake and on the same network, and that SSH is switched on (in Desktop Mode run: sudo systemctl enable --now sshd).")
	}
	defer conn.Close()
	pass("Connect to the SSH port ("+strconv.Itoa(port)+")", "Connected.")

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	line = strings.TrimSpace(clean(line))
	if err != nil && line == "" {
		return fail("SSH server answers", "The port is open but no SSH server said hello.")
	}
	if !strings.HasPrefix(line, "SSH-") {
		return fail("SSH server answers", "Something answered on that port, but it does not look like SSH.")
	}
	pass("SSH server answers", line)
	return steps
}

func clean(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 0x20 && r != 0x7f {
			b.WriteRune(r)
		}
	}
	out := b.String()
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}
