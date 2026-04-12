package stt

import (
	"os/exec"
	"strings"
)

type Notifier struct {
	lastID string
}

func (n *Notifier) Show(urgency, title, body string) error {
	args := []string{"--printid", "--timeout", "0", "-u", urgency} // 0 = no auto-dismiss
	if n.lastID != "" {
		args = append(args, "-r", n.lastID)
	}
	args = append(args, title)
	if body != "" {
		args = append(args, body)
	}

	out, err := exec.Command("dunstify", args...).Output()
	if err != nil {
		return err
	}
	n.lastID = strings.TrimSpace(string(out))
	return nil
}

func (n *Notifier) Close() error {
	if n.lastID == "" {
		return nil
	}
	err := exec.Command("dunstify", "-C", n.lastID).Run()
	n.lastID = ""
	return err
}
