//go:build linux

package agent

import (
	"bufio"
	"os"
	"strconv"
	"strings"
)

// listeningPorts reads /proc/net/{tcp,tcp6,udp,udp6}: TCP sockets in LISTEN
// state (0A) and every bound UDP socket.
func listeningPorts() map[int]bool {
	out := map[int]bool{}
	for _, f := range []string{"/proc/net/tcp", "/proc/net/tcp6", "/proc/net/udp", "/proc/net/udp6"} {
		file, err := os.Open(f)
		if err != nil {
			continue
		}
		tcp := strings.Contains(f, "tcp")
		sc := bufio.NewScanner(file)
		sc.Scan() // header
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 {
				continue
			}
			if tcp && fields[3] != "0A" {
				continue
			}
			_, portHex, ok := strings.Cut(fields[1], ":")
			if !ok {
				continue
			}
			if p, err := strconv.ParseInt(portHex, 16, 32); err == nil {
				out[int(p)] = true
			}
		}
		file.Close()
	}
	return out
}
