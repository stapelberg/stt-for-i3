package stt

import "strconv"

type State int

const (
	Idle State = iota
	Recording
	Transcribing
)

func (s State) String() string {
	names := [...]string{"idle", "recording", "transcribing"}
	if int(s) < 0 || int(s) >= len(names) {
		return "State(" + strconv.Itoa(int(s)) + ")"
	}
	return names[s]
}
