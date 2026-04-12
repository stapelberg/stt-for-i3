package stt

import (
	"net"
	"os"
)

func sdNotify(state string) error {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return nil // not running under systemd
	}
	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return err
	}
	defer conn.Close()
	_, err = conn.Write([]byte(state))
	return err
}
