// Package daemon is the supervisor and the proxy behind ilot. It knows nothing about stacks, configuration or git.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
)

type Spec struct {
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	// The whole environment of the process: the daemon adds nothing of its own.
	Env     map[string]string `json:"env,omitempty"`
	LogFile string            `json:"log_file"`
	// Runs instead of SIGTERM when the process stops.
	Stop string `json:"stop,omitempty"`
	// With no probe, a process is ready as soon as it runs.
	Ready *Probe `json:"ready,omitempty"`
}

func (spec Spec) Validate() error {
	if spec.Command == "" {
		return errors.New("the specification has no command")
	}
	if spec.LogFile == "" {
		return errors.New("the specification has no log file")
	}
	if spec.Ready != nil {
		return spec.Ready.Validate()
	}
	return nil
}

// Hash is stable across daemon restarts, because encoding/json sorts map keys.
func (spec Spec) Hash() string {
	encoded, err := json.Marshal(spec)
	if err != nil {
		panic("daemon: a specification always encodes: " + err.Error())
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

type State string

const (
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateStopping State = "stopping"
	StateExited   State = "exited"
)

type Process struct {
	Name     string `json:"name"`
	State    State  `json:"state"`
	PID      int    `json:"pid,omitzero"`
	ExitCode int    `json:"exit_code"`
	Hash     string `json:"hash"`
}
