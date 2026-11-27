// Package daemon is the supervisor and the proxy behind ilot. It knows nothing about stacks, configuration or git.
package daemon

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

type Spec struct {
	Command string `json:"command"`
	Dir     string `json:"dir,omitempty"`
	// The whole environment of the process, with Session: the daemon adds nothing of its own.
	Env map[string]string `json:"env,omitempty"`
	// Variables of the terminal of the command that sent the specification, such as LANG or SSH_AUTH_SOCK.
	// The process gets them below Env, and the hash leaves them out, so a command from another terminal does not replace the process.
	Session map[string]string `json:"session,omitempty"`
	LogFile string            `json:"log_file"`
	// Runs instead of SIGTERM when the process stops.
	Stop string `json:"stop,omitempty"`
	// With no probe, a process is ready when it still runs after a short time.
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

func validateName(name string) error {
	switch name {
	case "":
		return errors.New("the process has no name")
	case ".", "..":
		// The HTTP router reads these as folders of the path, so no call would reach the process.
		return fmt.Errorf("the process name %q is not allowed", name)
	}
	return nil
}

// Hash is stable across daemon restarts, because encoding/json sorts map keys.
func (spec Spec) Hash() string {
	spec.Session = nil
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
