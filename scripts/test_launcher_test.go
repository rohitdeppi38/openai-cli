//go:build !windows

package scripts_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestLauncherRequiresHealthyMock(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is required by the test launcher")
	}
	launcher, err := os.ReadFile("test")
	if err != nil {
		t.Fatal(err)
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
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			for _, dir := range []string{bin, filepath.Join(root, "scripts")} {
				if err := os.Mkdir(dir, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			files := map[string]string{
				"scripts/test": string(launcher),
				// Preserve the launcher's curl options while directing its fixed
				// health endpoint to this test's ephemeral loopback listener.
				"bin/curl": `#!/usr/bin/env bash
args=("$@")
exec "$TEST_REAL_CURL" --noproxy '*' "${args[@]:0:${#args[@]}-1}" "$TEST_HEALTH_URL/_x-steady/health"
`,
				"bin/go": `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$TEST_GO_LOG"
`,
				"bin/lsof": "#!/usr/bin/env bash\nexit 0\n",
				// No dependency installation or daemon is needed for this test.
				"scripts/mock": "#!/usr/bin/env bash\nexit 23\n",
			}
			for name, contents := range files {
				if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			log := filepath.Join(root, "go.log")
			command := exec.Command("bash", filepath.Join(root, "scripts", "test"))
			command.Env = append(os.Environ(),
				"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
				"TEST_API_BASE_URL=",
				"TEST_REAL_CURL="+curl,
				"TEST_HEALTH_URL="+server.URL,
				"TEST_GO_LOG="+log,
			)
			output, err := command.CombinedOutput()
			if status == http.StatusOK {
				if err != nil {
					t.Fatalf("healthy mock rejected: %v; output:\n%s", err, output)
				}
				calls, err := os.ReadFile(log)
				if err != nil || strings.TrimSpace(string(calls)) != "test ./...\ntest -c ./..." {
					t.Fatalf("healthy mock did not run test commands: %q, %v", calls, err)
				}
			} else {
				if err == nil {
					t.Fatalf("unhealthy mock was accepted; output:\n%s", output)
				}
				if _, err := os.Stat(log); !os.IsNotExist(err) {
					t.Fatalf("test commands ran without a healthy mock: %v", err)
				}
			}
		})
	}
}
