package emitscan_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/full-chaos/dev-health-acr/internal/cigates/emitscan"
)

// estimateOnlyShape marks a shape the helper alone would let through (it finishes inside its limits, and the in-process re-run is
// then slow): only the syntax estimate refuses it.
const estimateOnlyShape = "[estimate only]"

const childRefusal = "R10:refusal:typecheck,R9:unresolvable key"

// The shapes below are ones the checker folds at a cost nothing the scan counts bounds: the type-check helper's own limits
// refuse them.
func checkerHeavyShapes() []keyCase {
	trig := "\nfunc f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	a64 := strings.Repeat("a", 64)
	doublingMax := func(n int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "const c0 = %q\n", a64)
		for i := 1; i <= n; i++ {
			fmt.Fprintf(&b, "const c%d = max(c%d, \"\") + max(c%d, \"\")\n", i, i-1, i-1)
		}
		return b.String()
	}
	var p16, p21, p01b, p20 strings.Builder
	p16.WriteString(doublingMax(22))
	for i := 0; i < 40; i++ {
		fmt.Fprintf(&p16, "const d%d = max(c22 + \"%d\", \"\")\n", i, i)
	}
	p21.WriteString(doublingMax(20))
	for i := 0; i < 30; i++ {
		fmt.Fprintf(&p21, "const d%d = max(c20 + \"%d\", \"\")\n", i, i)
	}
	fmt.Fprintf(&p01b, "const c0 = %q\n", a64)
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&p01b, "const c%d = min(c%d + c%d, \"zzzz\" + c%d + c%d)\n", i, i-1, i-1, i-1, i-1)
	}
	digits := "0." + strings.Repeat("123456789", 133)
	fmt.Fprintf(&p20, "const p = %s\nconst q = %s\nconst z = complex(p, q)\nconst y = complex(q, p)\n", digits, digits)
	for i := 0; i < 450; i++ {
		fmt.Fprintf(&p20, "const r%d = %s\n", i, chainOf("z*y", "-", 60))
	}
	shortLiteralComplex := func(exp int) string {
		var b strings.Builder
		fmt.Fprintf(&b, "const p = (1e%d + 1) * 1e-1190\nconst q = (3e%d + 7) * 1e-1189\nconst z = complex(p, q)\nconst y = complex(q, p)\n", exp, exp-1)
		for i := 0; i < 190; i++ {
			fmt.Fprintf(&b, "const r%d = %s\n", i, chainOf("z*y", "-", 60))
		}
		return b.String()
	}
	return []keyCase{
		{estimateOnlyShape + " p24 short literals, exponent 600, complex chains", shortLiteralComplex(600) + trig, childRefusal},
		{estimateOnlyShape + " p26 short literals, exponent 1180, complex chains", shortLiteralComplex(1180) + trig, childRefusal},
		{"max doubling to 256 MB strings x40 (memory)", p16.String() + trig, childRefusal},
		{"max doubling to 64 MB strings x30", p21.String() + trig, childRefusal},
		{"min with an inner + doubling x40 (memory)", p01b.String() + trig, childRefusal},
		{"1200-digit complex rationals in 450 chains of 60 terms (cpu)", p20.String() + trig, childRefusal},
	}
}

func TestKeyScopeCheckerHeavyShapesAreRefusedByTheHelperLimits(t *testing.T) {
	for _, estimate := range []bool{true, false} {
		restore := emitscan.SetCostEstimate(estimate)
		runHeavyShapes(t, estimate)
		restore()
	}
}

func runHeavyShapes(t *testing.T, estimate bool) {
	for _, c := range checkerHeavyShapes() {
		if !estimate && strings.HasPrefix(c.name, estimateOnlyShape) {
			continue
		}
		start := cpuTime(t)
		wall := time.Now()
		got := keyFindings(scanSource(t, header+c.body+"\n"))
		used := cpuTime(t) - start
		t.Logf("%s (estimate %v): parent %s cpu, %s wall, findings %q", c.name, estimate, used, time.Since(wall).Round(time.Millisecond), got)
		if used > time.Second {
			t.Errorf("%s: the scanning process used %s cpu, want < 1s even under the race detector", c.name, used)
		}
		if estimate && !strings.Contains(c.name, "1200-digit") && time.Since(wall) > 300*time.Millisecond {
			t.Errorf("%s: took %s with the syntax estimate on, which must refuse it before any helper runs", c.name, time.Since(wall))
		}
		if got != c.want {
			t.Errorf("%s: key findings %q want %q", c.name, got, c.want)
		}
	}
}

func plantHelper(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "helper.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// ackScript is the start of a planted helper that reads the request and has the acknowledgement line of exactly that request.
const ackScript = `payload=$(cat); sum=$(printf %s "$payload" | sha256sum | cut -d' ' -f1); ack="emitscan-tcheck-ok $sum"
`

func TestKeyScopeAnyAbnormalHelperResultIsARefusalNeverAnInProcessCheck(t *testing.T) {
	// a key identifier that the in-process check would read (R1:api_token) if it ever ran
	body := "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	cases := []struct {
		name, script string
		wall         time.Duration
	}{
		{"non-zero exit with no output", `cat >/dev/null; exit 3`, 0},
		{"the right acknowledgement, then a non-zero exit", ackScript + `echo "$ack"; exit 3`, 0},
		{"the right acknowledgement, then killed by a signal", ackScript + `echo "$ack"; kill -9 $$`, 0},
		{"garbage output", `cat >/dev/null; echo garbage`, 0},
		{"empty output", `cat >/dev/null; exit 0`, 0},
		{"truncated acknowledgement", ackScript + `printf %s "${ack%??????????}"`, 0},
		{"acknowledgement without its newline", ackScript + `printf %s "$ack"`, 0},
		{"acknowledgement of another request", `cat >/dev/null; echo "emitscan-tcheck-ok 0000000000000000000000000000000000000000000000000000000000000000"`, 0},
		{"acknowledgement and extra output", ackScript + `echo "$ack"; echo extra`, 0},
		{"output beyond the cap", ackScript + `echo "$ack"; head -c 100000 /dev/zero | tr '\0' x`, 0},
		{"too slow: the right acknowledgement after the wall-clock limit", ackScript + `sleep 3; echo "$ack"`, 300 * time.Millisecond},
	}
	for _, c := range cases {
		restoreHelper := emitscan.SetHelperBinary(plantHelper(t, c.script))
		restoreLimits := func() {}
		if c.wall > 0 {
			restoreLimits = emitscan.SetChildLimits(4, 512<<20, c.wall)
		}
		got := keyFindings(scanSource(t, header+body+"\n"))
		restoreLimits()
		restoreHelper()
		if got != childRefusal {
			t.Errorf("%s: key findings %q want %q", c.name, got, childRefusal)
		}
	}
	// control: the right acknowledgement and a zero exit is a pass, and then the package is read by types
	restore := emitscan.SetHelperBinary(plantHelper(t, ackScript+`echo "$ack"`))
	defer restore()
	if got := keyFindings(scanSource(t, header+body+"\n")); got != "R1:api_token" {
		t.Errorf("control: key findings %q want R1:api_token", got)
	}
}

func TestKeyScopeHelperMemoryLimitIsARefusal(t *testing.T) {
	restore := emitscan.SetChildLimits(4, 1<<20, 20*time.Second)
	defer restore()
	body := "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	if got := keyFindings(scanSource(t, header+body+"\n")); got != childRefusal {
		t.Errorf("a helper over its resident-set limit: key findings %q want %q", got, childRefusal)
	}
}

func TestKeyScopeHelperGetsOnlyAnAllowlistedEnvironment(t *testing.T) {
	t.Setenv("EMITSCAN_TEST_SECRET_TOKEN", "never-printed")
	t.Setenv("GITHUB_TOKEN", "never-printed")
	t.Setenv("ANTHROPIC_API_KEY", "never-printed")
	out, err := emitscan.HelperEnvNames(benignRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	names := strings.Split(strings.TrimSpace(strings.TrimPrefix(out, "env ")), ",")
	allowed := map[string]bool{"GOMEMLIMIT": true, "GOENV": true, "GOWORK": true}
	for _, name := range names {
		if name != "" && !allowed[name] {
			t.Errorf("the helper received the variable %s, which is not on the allowlist", name)
		}
	}
	have := map[string]bool{}
	for _, name := range names {
		have[name] = true
	}
	for name := range allowed {
		if !have[name] {
			t.Errorf("the helper did not receive %s, which fixes its go environment", name)
		}
	}
	for _, name := range names {
		if strings.Contains(name, "TOKEN") || strings.Contains(name, "KEY") || strings.Contains(name, "SECRET") {
			t.Errorf("the helper received the credential-like variable %s", name)
		}
	}
}

func TestKeyScopeHelperLimitsKeepHeadroomOverTheRealTree(t *testing.T) {
	if _, err := emitscan.Scan(benignRepoRoot(t), []string{"internal", "cmd"}); err != nil {
		t.Fatal(err)
	}
	cpu, rssKB, helpers := emitscan.ChildStats()
	limitCPU, limitRSS, _ := emitscan.ChildLimits()
	t.Logf("real tree: %d helpers, largest cpu %s, largest resident set %d MB; limits: cpu %ds, resident set %d MB", helpers, time.Duration(cpu), rssKB>>10, limitCPU, limitRSS>>20)
	if helpers == 0 {
		t.Fatal("no helper ran: the headroom check reads nothing")
	}
	if time.Duration(cpu)*3 > time.Duration(limitCPU)*time.Second {
		t.Errorf("the cpu limit %ds is under 3x the largest helper (%s)", limitCPU, time.Duration(cpu))
	}
	if rssKB<<10*3 > limitRSS {
		t.Errorf("the resident-set limit %d MB is under 3x the largest helper (%d MB)", limitRSS>>20, rssKB>>10)
	}
}

const childUnavailable = "R10:refusal:typecheck_child_unavailable,R9:unresolvable key"

func TestKeyScopeUnavailableHelperRefusesEveryIdentifierKey(t *testing.T) {
	body := "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v) }"
	restore := emitscan.SetHelperBuilder(func() (string, error) { return "", fmt.Errorf("go: toolchain not found") })
	if got := keyFindings(scanSource(t, header+body+"\n")); got != childUnavailable {
		t.Errorf("an unbuildable helper: key findings %q want %q", got, childUnavailable)
	}
	if got := keyFindings(scanSource(t, header+"func f(l *slog.Logger, v string) { l.Info(\"m\", \"api_token\", v) }\n")); got != "R1:api_token" {
		t.Errorf("an unbuildable helper, literal key: key findings %q want R1:api_token", got)
	}
	restore()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "real.sh")
	if err := os.WriteFile(target, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{"a symlink": link, "a missing file": filepath.Join(dir, "missing")} {
		restore := emitscan.SetHelperBinary(path)
		got := keyFindings(scanSource(t, header+body+"\n"))
		restore()
		if got != childUnavailable {
			t.Errorf("%s as the helper: key findings %q want %q", name, got, childUnavailable)
		}
	}
	// control: the real helper is back
	if got := keyFindings(scanSource(t, header+body+"\n")); got != "R1:api_token" {
		t.Errorf("control: key findings %q want R1:api_token", got)
	}
}

func TestKeyScopeHelperLivesInAPrivateDirectoryOfItsOwn(t *testing.T) {
	path, err := emitscan.HelperPath(benignRepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("the helper %s is not a regular file: %v", path, err)
	}
	dirInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil || !dirInfo.IsDir() || dirInfo.Mode().Perm() != 0o700 {
		t.Fatalf("the helper directory is not 0700: %v %v", dirInfo, err)
	}
	if strings.Contains(path, "stable") || !strings.Contains(filepath.Base(filepath.Dir(path)), "emitscan-tcheck-") {
		t.Errorf("unexpected helper location %s", path)
	}
	emitscan.ShutdownHelper()
	if _, err := os.Lstat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Errorf("the helper directory survived ShutdownHelper: %v", err)
	}
}

func TestKeyScopeHelperBuildEnvironmentIsFixed(t *testing.T) {
	t.Setenv("GOFLAGS", "-race -toolexec=/bin/true")
	t.Setenv("GOPROXY", "https://example.invalid")
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOTOOLCHAIN", "auto")
	got := map[string]string{}
	for _, entry := range emitscan.BuildEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		got[name] = value
	}
	for name, want := range map[string]string{"GOFLAGS": "", "GOENV": "off", "GOWORK": "off", "CGO_ENABLED": "0", "GOTOOLCHAIN": "local", "GOPROXY": "off"} {
		if value, ok := got[name]; !ok || value != want {
			t.Errorf("the helper's build has %s set to %q (present %v), want %q", name, value, ok, want)
		}
	}
	for name := range got {
		if strings.Contains(name, "TOKEN") || strings.Contains(name, "SECRET") || strings.Contains(name, "KEY") {
			t.Errorf("the helper's build received the credential-like variable %s", name)
		}
	}
}

func TestKeyScopeSyntaxEstimateRefusesHugeNumericLiterals(t *testing.T) {
	for src, want := range map[string]bool{
		"1e1180": true, "1e-1190": true, "3e599": true, "1e300": false, "1e-300": false, "1e301": true, "0.5": false, "2.5e10": false,
		"1_000_000": false, "3.14159265358979323846264338327950288419716939937510582097494459": true,
		"0.123456789012345678901234567890123456789": false, "0xdeadbeef": false, "0x1p-1100": true, "0x1p100": false,
		"1e1180i": true, "'e'": false, "'x'": false, "123456789012345678901234567890123456789012345678901": true, "7": false,
	} {
		if got := emitscan.LiteralHuge(src); got != want {
			t.Errorf("LiteralHuge(%q) = %v want %v", src, got, want)
		}
	}
}

func TestKeyScopeHelperMemoryPeakCounts(t *testing.T) {
	for _, c := range []struct {
		rss, peak, limit int64
		want             bool
	}{
		{100, 100, 200, false}, {300, 300, 200, true}, {100, 300, 200, true}, {300, 100, 200, true}, {200, 200, 200, false},
	} {
		if got := emitscan.MemoryExceeded(c.rss, c.peak, c.limit); got != c.want {
			t.Errorf("MemoryExceeded(rss %d, peak %d, limit %d) = %v want %v", c.rss, c.peak, c.limit, got, c.want)
		}
	}
}

func TestKeyScopeHelperCPUBudgetOfOneScanRefusesTheRest(t *testing.T) {
	burn := plantHelper(t, ackScript+`i=0; while [ $i -lt 300000 ]; do i=$((i+1)); done; echo "$ack"`)
	restoreHelper := emitscan.SetHelperBinary(burn)
	defer restoreHelper()
	restoreBudget := emitscan.SetScanHelperCPU(50 * time.Millisecond)
	defer restoreBudget()
	root := t.TempDir()
	for _, name := range []string{"a", "b", "c"} {
		dir := filepath.Join(root, "internal", name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		source := header + "func f(l *slog.Logger, v string) { key := \"api_token\"; l.Info(\"m\", key, v); _ = \"" + name + "\" }\n"
		if err := os.WriteFile(filepath.Join(dir, "x.go"), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	findings, err := emitscan.Scan(root, []string{"internal"})
	if err != nil {
		t.Fatal(err)
	}
	var typed, refused int
	for _, f := range findings {
		switch {
		case f.Rule == emitscan.RuleProtectedKey && f.Detail == "api_token":
			typed++
		case f.Rule == emitscan.RuleBenignKind && f.Detail == "refusal:typecheck":
			refused++
		}
	}
	if typed == 0 || refused == 0 || typed+refused != 3 {
		t.Errorf("with a 50 ms budget for all helpers of the scan: %d packages typed, %d refused, want at least one of each and 3 in all", typed, refused)
	}
}

func TestKeyScopeHelperBuildIgnoresTheGoEnvFile(t *testing.T) {
	read := func(name string) string {
		out, err := exec.Command("go", "env", name).Output()
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out))
	}
	cache, modCache, goPath, goRoot := read("GOCACHE"), read("GOMODCACHE"), read("GOPATH"), read("GOROOT")
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".config", "go"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".config", "go", "env"), []byte("GOFLAGS=-toolexec=/bin/false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("GOCACHE", cache)
	t.Setenv("GOMODCACHE", modCache)
	t.Setenv("GOPATH", goPath)
	t.Setenv("GOROOT", goRoot)
	t.Setenv("GOENV", "")
	t.Cleanup(emitscan.ShutdownHelper)
	emitscan.ShutdownHelper()
	restore := emitscan.SetHelperBuilder(nil)
	defer restore()
	if _, err := emitscan.HelperPath(benignRepoRoot(t)); err != nil {
		t.Errorf("the helper did not build with a go env file that sets GOFLAGS=-toolexec: %v", err)
	}
	emitscan.ShutdownHelper()
}
