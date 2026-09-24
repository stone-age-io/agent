package nats

import (
	"encoding/json"
	"errors"
	"testing"
)

// The cmd.exec reply, checked as the JSON a caller receives: which fields are
// present is the contract, so a Go-level comparison would miss the thing that
// matters. exit_code present means the command ran; output rides along with
// every failure that has any.
func TestExecResponse(t *testing.T) {
	tests := []struct {
		name     string
		output   string
		exitCode int
		err      error

		wantStatus   string
		wantExitCode any // nil means absent
		wantOutput   any // nil means absent
	}{
		{
			name:         "refused",
			exitCode:     -1,
			err:          errors.New("command not in allowed list or scripts directory"),
			wantStatus:   "error",
			wantExitCode: nil,
			wantOutput:   nil,
		},
		{
			name:         "success with output",
			output:       "all good\n",
			exitCode:     0,
			wantStatus:   "success",
			wantExitCode: float64(0),
			wantOutput:   "all good\n",
		},
		{
			// exit_code used to be omitempty, so success dropped it
			name:         "success with no output",
			exitCode:     0,
			wantStatus:   "success",
			wantExitCode: float64(0),
			wantOutput:   "",
		},
		{
			// The reason this function exists: this reply used to be only
			// "command exited with code 3"
			name:         "non-zero exit keeps output and exit code",
			output:       "partial\nSTDERR:\nboom\n",
			exitCode:     3,
			err:          errors.New("command exited with code 3"),
			wantStatus:   "error",
			wantExitCode: float64(3),
			wantOutput:   "partial\nSTDERR:\nboom\n",
		},
		{
			name:         "timeout keeps partial output, has no exit code",
			output:       "started\n",
			exitCode:     -1,
			err:          errors.New("command execution timeout (30s)"),
			wantStatus:   "error",
			wantExitCode: nil,
			wantOutput:   "started\n",
		},
		{
			name:         "JSON output is embedded as JSON",
			output:       "{\"pools\":2}\n",
			exitCode:     0,
			wantStatus:   "success",
			wantExitCode: float64(0),
			wantOutput:   map[string]any{"pools": float64(2)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw, err := json.Marshal(execResponse("check.sh", tt.output, tt.exitCode, tt.err))
			if err != nil {
				t.Fatal(err)
			}
			var got map[string]any
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatal(err)
			}

			if got["status"] != tt.wantStatus {
				t.Errorf("status = %v, want %v", got["status"], tt.wantStatus)
			}
			if got["command"] != "check.sh" {
				t.Errorf("command = %v, want check.sh", got["command"])
			}
			if (tt.err != nil) != (got["error"] != nil) {
				t.Errorf("error = %v, want present=%v", got["error"], tt.err != nil)
			}
			assertField(t, got, "exit_code", tt.wantExitCode)
			assertField(t, got, "output", tt.wantOutput)
		})
	}
}

func assertField(t *testing.T, got map[string]any, key string, want any) {
	t.Helper()
	value, present := got[key]
	if want == nil {
		if present {
			t.Errorf("%s = %v, want absent", key, value)
		}
		return
	}
	if !present {
		t.Errorf("%s absent, want %v", key, want)
		return
	}
	wantJSON, _ := json.Marshal(want)
	gotJSON, _ := json.Marshal(value)
	if string(wantJSON) != string(gotJSON) {
		t.Errorf("%s = %s, want %s", key, gotJSON, wantJSON)
	}
}
