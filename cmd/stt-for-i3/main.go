package main

import (
	"bufio"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stapelberg/stt-for-i3/internal/stt"
)

func socketPath() (string, error) {
	runtime := os.Getenv("XDG_RUNTIME_DIR")
	if runtime == "" {
		return "", fmt.Errorf("XDG_RUNTIME_DIR is not set; cannot determine secure socket path")
	}
	return filepath.Join(runtime, "stt-for-i3.sock"), nil
}

func toggle(sp string) error {
	conn, err := net.Dial("unix", sp)
	if err != nil {
		return fmt.Errorf("connect to daemon: %w (is stt-for-i3 daemon running?)", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := fmt.Fprintln(conn, "toggle"); err != nil {
		return fmt.Errorf("send toggle: %w", err)
	}

	scanner := bufio.NewScanner(conn)
	if scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "error ") {
			return fmt.Errorf("daemon: %s", strings.TrimPrefix(line, "error "))
		}
		// ok <state> — nothing to print
	}
	return scanner.Err()
}

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: stt-for-i3 <daemon|toggle>\n")
		os.Exit(1)
	}

	sp, err := socketPath()
	if err != nil {
		log.Fatal(err)
	}

	switch os.Args[1] {
	case "daemon":
		d, err := stt.NewDaemon(sp)
		if err != nil {
			log.Fatal(err)
		}
		if err := d.Run(); err != nil {
			log.Fatal(err)
		}
	case "toggle":
		if err := toggle(sp); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}
