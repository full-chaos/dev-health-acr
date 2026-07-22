package nativeadapters

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestBuildExactClientContracts(t *testing.T) {
	roots := Roots{Home: "/tmp/home", Config: "/tmp/config", Work: "/tmp/work", Sidecar: "/tmp/bin/acr-mcp"}
	cases := []struct {
		client Client
		args   []string
	}{
		{OpenCode, []string{"run", "--format", "json", Prompt}},
		{Claude, []string{"--print", "--output-format", "stream-json", Prompt}},
		{Codex, []string{"exec", "--json", Prompt}},
		{Cursor, []string{"-p", "--output-format", "json", Prompt}},
	}
	for _, test := range cases {
		t.Run(string(test.client), func(t *testing.T) {
			invocation, err := Build(test.client, "/bin/client", roots)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(invocation.Args, test.args) {
				t.Fatalf("args = %#v", invocation.Args)
			}
			if invocation.Dir != roots.Work || invocation.Env[0] != "HOME=/tmp/home" || invocation.Env[len(invocation.Env)-1] != "PATH=/tmp/bin:/usr/bin:/bin" {
				t.Fatalf("unsafe invocation: %#v", invocation)
			}
		})
	}
}

func TestRunDeadlineOutputLimitAndRedaction(t *testing.T) {
	if os.Getenv("NATIVE_ADAPTER_HELPER") == "1" {
		switch os.Getenv("NATIVE_ADAPTER_MODE") {
		case "hang":
			for {
				time.Sleep(time.Second)
			}
		case "overflow":
			for range maxOutput + 1 {
				_, _ = os.Stdout.Write([]byte("x"))
			}
		}
		os.Exit(0)
	}
	roots := Roots{Home: "/tmp/home", Config: "/tmp/config", Work: t.TempDir(), Sidecar: "/tmp/bin/acr-mcp"}
	for _, test := range []struct {
		mode    string
		want    string
		timeout time.Duration
	}{{"hang", context.DeadlineExceeded.Error(), 50 * time.Millisecond}, {"overflow", ErrOutputLimit.Error(), 5 * time.Second}} {
		t.Run(test.mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), test.timeout)
			defer cancel()
			invocation := Invocation{Client: Codex, Binary: os.Args[0], Args: []string{"-test.run=TestRunDeadlineOutputLimitAndRedaction"}, Env: append(os.Environ(), "NATIVE_ADAPTER_HELPER=1", "NATIVE_ADAPTER_MODE="+test.mode), Dir: roots.Work}
			_, err := Run(ctx, invocation)
			if err == nil || !bytes.Contains([]byte(err.Error()), []byte(test.want)) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if got := Redact("not-a-secret /tmp/home /tmp/config /tmp/work /tmp/bin/acr-mcp", Roots{Home: "/tmp/home", Config: "/tmp/config", Work: "/tmp/work", Sidecar: "/tmp/bin/acr-mcp"}); got != "[REDACTED] [ISOLATED_PATH] [ISOLATED_PATH] [ISOLATED_PATH] [ISOLATED_PATH]" {
		t.Fatalf("redaction = %q", got)
	}
}

func TestBuildRejectsAmbientRoots(t *testing.T) {
	_, err := Build(OpenCode, "/bin/client", Roots{Home: "/tmp/home", Config: "relative", Work: "/tmp/work", Sidecar: "/tmp/bin/acr-mcp"})
	if err == nil {
		t.Fatal("Build accepted relative root")
	}
}

func TestParsePerClientGoldenAndFailures(t *testing.T) {
	for _, client := range []Client{OpenCode, Claude, Codex, Cursor} {
		t.Run(string(client), func(t *testing.T) {
			output, err := os.ReadFile(filepath.Join("testdata", string(client)+".jsonl"))
			if err != nil {
				t.Fatal(err)
			}
			if err := Parse(client, output); err != nil {
				t.Fatal(err)
			}
			for _, bad := range [][]byte{[]byte("not-json"), []byte(join(recordingEvents(client)) + "\n{}"), []byte(`{"type":"error"}`)} {
				if err := Parse(client, bad); err == nil {
					t.Fatalf("accepted malformed output %q", bad)
				}
			}
		})
	}
}

func TestRecordCapturesAllowlistedEnvironmentAndExactEvents(t *testing.T) {
	var output bytes.Buffer
	err := Record(Codex, []string{"exec", "--json"}, []string{"HOME=/tmp/home", "XDG_CONFIG_HOME=/tmp/config", "PATH=/usr/bin:/bin", "SECRET=hidden"}, filepath.Clean("/tmp/work"), &output)
	if err != nil {
		t.Fatal(err)
	}
	if err := Parse(Codex, output.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordingStubAcceptsAndCapturesInvocation(t *testing.T) {
	records := t.TempDir()
	command := exec.Command("go", "run", "../../cmd/native-client-recording-stub", "codex", "exec", "--json")
	command.Env = append(os.Environ(), "ACR_NATIVE_RECORDS="+records)
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	if err := Parse(Codex, output); err != nil {
		t.Fatal(err)
	}
	recording, err := os.ReadFile(filepath.Join(records, "codex.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(recording, []byte(`"args":["exec","--json"]`)) || !bytes.Contains(recording, []byte(`"config":{"mcpServers"`)) {
		t.Fatalf("recording = %s", recording)
	}
}

func join(lines []string) string {
	var output bytes.Buffer
	for _, line := range lines {
		output.WriteString(line)
		output.WriteByte('\n')
	}
	return output.String()
}
