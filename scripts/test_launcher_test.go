//go:build !windows

package scripts_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestLauncherRequiresHealthyMock(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required by the test launcher")
	}
	for _, status := range []int{http.StatusOK, http.StatusNotFound, http.StatusInternalServerError} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/_x-steady/health" {
					t.Errorf("unexpected health path: %s", r.URL.Path)
				}
				w.WriteHeader(status)
			}))
			defer server.Close()
			command, root := newLauncherFixture(t, `args=("$@")
exec "$TEST_REAL_CURL" --noproxy '*' "${args[@]:0:${#args[@]}-1}" "$TEST_HEALTH_URL/_x-steady/health"
`, `# Even an incomplete handoff from a failed startup must not be trusted.
if [[ "${2:-}" == --pid-file ]]; then printf '424241\n' > "$3"; fi
exit 23
`)
			command.Env = append(command.Env, "TEST_REAL_CURL="+curl, "TEST_HEALTH_URL="+server.URL)
			output, err := command.CombinedOutput()
			if status == http.StatusOK {
				if err != nil {
					t.Fatalf("healthy mock rejected: %v; output:\n%s", err, output)
				}
				calls, err := os.ReadFile(filepath.Join(root, "go.log"))
				if err != nil || strings.TrimSpace(string(calls)) != "test ./...\ntest -c ./..." {
					t.Fatalf("healthy mock did not run test commands: %q, %v", calls, err)
				}
			} else {
				if err == nil {
					t.Fatalf("unhealthy mock was accepted; output:\n%s", output)
				}
				if _, err := os.Stat(filepath.Join(root, "go.log")); !os.IsNotExist(err) {
					t.Fatalf("test commands ran without a healthy mock: %v", err)
				}
			}
			// lsof reports a foreign PID. The kill spy never sends real signals.
			if data, err := os.ReadFile(filepath.Join(root, "kill.log")); !os.IsNotExist(err) {
				t.Fatalf("launcher attempted to stop an existing listener: %q, %v", data, err)
			}
		})
	}
}

func TestLauncherStopsOnlyRecordedMock(t *testing.T) {
	for _, exitCode := range []int{0, 37} {
		t.Run(fmt.Sprint(exitCode), func(t *testing.T) {
			command, root := newLauncherFixture(t, `if [[ ! -e "$TEST_CURL_SEEN" ]]; then
  touch "$TEST_CURL_SEEN"
  exit 7
fi
`, `[[ "$1" == --daemon && "$2" == --pid-file ]] || exit 24
printf '424242\n' > "$3"
printf '%s\n' "$3" > "$TEST_PID_PATH"
`)
			command.Env = append(command.Env, fmt.Sprintf("TEST_GO_EXIT=%d", exitCode))
			output, err := command.CombinedOutput()
			gotExit := 0
			if failure, ok := err.(*exec.ExitError); ok {
				gotExit = failure.ExitCode()
			} else if err != nil {
				t.Fatal(err)
			}
			if gotExit != exitCode {
				t.Fatalf("launcher exit = %d, want %d; output:\n%s", gotExit, exitCode, output)
			}
			data, err := os.ReadFile(filepath.Join(root, "kill.log"))
			if err != nil || string(data) != "424242\n" {
				t.Fatalf("cleanup did not stop only the recorded mock: %q, %v", data, err)
			}
			pidPath, err := os.ReadFile(filepath.Join(root, "pid.path"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(strings.TrimSpace(string(pidPath))); !os.IsNotExist(err) {
				t.Fatalf("PID handoff file was not removed: %v", err)
			}
		})
	}
}

func newLauncherFixture(t *testing.T, curl, mock string) (*exec.Cmd, string) {
	t.Helper()
	launcher, err := os.ReadFile("test")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeLauncherFiles(t, root, map[string]string{
		"scripts/test": string(launcher),
		"scripts/mock": "#!/usr/bin/env bash\n" + mock,
		"bin/curl":     "#!/usr/bin/env bash\n" + curl,
		"bin/go":       "#!/usr/bin/env bash\nprintf '%s\\n' \"$*\" >> \"$TEST_GO_LOG\"\nexit \"${TEST_GO_EXIT:-0}\"\n",
		"bin/lsof":     "#!/usr/bin/env bash\nprintf '424241\\n'\n",
		"bash.env":     "kill() { printf '%s\\n' \"$*\" >> \"$TEST_KILL_LOG\"; }\n",
	})
	command := exec.Command("bash", filepath.Join(root, "scripts", "test"))
	command.Env = append(os.Environ(),
		"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
		"BASH_ENV="+filepath.Join(root, "bash.env"), "TEST_API_BASE_URL=",
		"TEST_GO_LOG="+filepath.Join(root, "go.log"), "TEST_KILL_LOG="+filepath.Join(root, "kill.log"),
		"TEST_CURL_SEEN="+filepath.Join(root, "curl.seen"), "TEST_PID_PATH="+filepath.Join(root, "pid.path"),
	)
	return command, root
}

func TestMockDaemonOwnsStartup(t *testing.T) {
	mock, err := os.ReadFile("mock")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"ready", "legacy", "exited", "handoff-fails"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			childPID := filepath.Join(root, "child.pid")
			stopped := filepath.Join(root, "stopped")
			writeLauncherFiles(t, root, map[string]string{
				"scripts/mock":           string(mock),
				"scripts/steady/install": "#!/usr/bin/env bash\nprintf '%s\\n' \"$TEST_STEADY\"\n",
				"steady": `#!/usr/bin/env bash
trap 'touch "$TEST_STOPPED"; exit 0' TERM
printf '%s\n' "$$" > "$TEST_CHILD_PID"
if [[ "$TEST_STARTUP" == exited ]]; then exit 23; fi
while true; do sleep 0.01; done
`,
				"bin/curl": `#!/usr/bin/env bash
while [[ ! -s "$TEST_CHILD_PID" ]]; do sleep 0.01; done
if [[ "$TEST_STARTUP" == exited ]]; then
  while kill -0 "$(< "$TEST_CHILD_PID")" 2>/dev/null; do sleep 0.01; done
fi
# A listener can answer successfully after our child exits.
exit 0
`,
			})
			t.Cleanup(func() {
				// Only signal the synthetic child still owned by this fixture.
				if _, err := os.Stat(stopped); err == nil || mode == "exited" {
					return
				}
				if data, err := os.ReadFile(childPID); err == nil {
					if pid, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
						if process, err := os.FindProcess(pid); err == nil {
							_ = process.Kill()
						}
					}
				}
			})
			pidFile := filepath.Join(root, "mock.pid")
			if mode == "handoff-fails" {
				pidFile = filepath.Join(root, "missing", "mock.pid")
			}
			args := []string{filepath.Join(root, "scripts", "mock"), "synthetic.yml", "--daemon"}
			if mode != "legacy" {
				args = append(args, "--pid-file", pidFile)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", args...)
			command.Env = append(os.Environ(), "BASH_ENV=",
				"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_STEADY="+filepath.Join(root, "steady"), "TEST_CHILD_PID="+childPID,
				"TEST_STOPPED="+stopped, "TEST_STARTUP="+mode)
			output, err := command.CombinedOutput()
			if (err == nil) != (mode == "ready" || mode == "legacy") || ctx.Err() != nil {
				t.Fatalf("mock startup %s: %v; output:\n%s", mode, err, output)
			}
			if mode == "ready" {
				owned, err := os.ReadFile(childPID)
				if err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(pidFile)
				if err != nil || string(data) != string(owned) {
					t.Fatalf("handoff PID = %q, want %q: %v", data, owned, err)
				}
			} else if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
				t.Fatalf("unexpected PID handoff: %v", err)
			}
			if mode == "handoff-fails" {
				if _, err := os.Stat(stopped); err != nil {
					t.Fatalf("failed handoff did not stop its own child: %v", err)
				}
			}
		})
	}
}

func writeLauncherFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, contents := range files {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}
