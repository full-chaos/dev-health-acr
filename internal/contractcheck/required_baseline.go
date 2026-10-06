package contractcheck

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const publishedSchemaDir = "contracts"

// RequiredBaselineOptions configures the required-field baseline check.
type RequiredBaselineOptions struct {
	Root string
	Out  io.Writer
}

// CheckRequiredAgainstTag fails when a published schema lists a field in
// `required` that the same schema did not require at the previous prod roll
// tag (prod-rev<N>, else v*). A field is optional for one release first; it may become required in
// the next. With no tag the check passes and says so.
func CheckRequiredAgainstTag(options RequiredBaselineOptions) error {
	root, err := findRoot(options.Root)
	if err != nil {
		return err
	}
	out := options.Out
	if out == nil {
		out = io.Discard
	}
	tag, err := previousReleaseTag(root)
	if err != nil {
		return err
	}
	if tag == "" {
		fmt.Fprintln(out, "contractcheck required-baseline: NO RELEASE TAG found (prod-rev* or v* reachable from HEAD, not at HEAD); nothing compared, passing. Fetch tags in CI.")
		return nil
	}
	files, err := gitOutput(root, "ls-tree", "-r", "--name-only", tag, "--", publishedSchemaDir)
	if err != nil {
		return err
	}
	var problems []string
	compared := 0
	for _, name := range strings.Split(strings.TrimSpace(files), "\n") {
		if !strings.HasSuffix(name, ".schema.json") {
			continue
		}
		current, err := decodeJSONFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			// Deleted or renamed since the tag: removal is not a required-field change.
			continue
		}
		raw, err := gitOutput(root, "show", tag+":"+name)
		if err != nil {
			return err
		}
		var baseline any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if err := decoder.Decode(&baseline); err != nil {
			return fmt.Errorf("%s at %s: %w", name, tag, err)
		}
		compared++
		for _, added := range newlyRequired(baseline, current, "") {
			problems = append(problems, fmt.Sprintf("%s: field %q at %q is required now but was not required at %s; make it optional for one release first", name, added.field, added.path, tag))
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return fmt.Errorf("newly required fields:\n  %s", strings.Join(problems, "\n  "))
	}
	fmt.Fprintf(out, "contractcheck required-baseline: %d schemas compared against %s, no newly required fields\n", compared, tag)
	return nil
}

type addedRequired struct{ path, field string }

// newlyRequired walks current beside baseline. Where both hold an object at
// the same path, any `required` entry in current and not in baseline is
// reported. A path absent from the baseline is a new subschema and is skipped.
func newlyRequired(baseline, current any, path string) []addedRequired {
	var found []addedRequired
	switch cur := current.(type) {
	case map[string]any:
		base, ok := baseline.(map[string]any)
		if !ok {
			return nil
		}
		if list, ok := cur["required"].([]any); ok {
			had := map[string]bool{}
			if old, ok := base["required"].([]any); ok {
				for _, v := range old {
					if s, ok := v.(string); ok {
						had[s] = true
					}
				}
			}
			for _, v := range list {
				if s, ok := v.(string); ok && !had[s] {
					found = append(found, addedRequired{path: pathOrRoot(path), field: s})
				}
			}
		}
		keys := make([]string, 0, len(cur))
		for k := range cur {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if k == "required" {
				continue
			}
			if b, ok := base[k]; ok {
				found = append(found, newlyRequired(b, cur[k], path+"/"+k)...)
			}
		}
	case []any:
		base, ok := baseline.([]any)
		if !ok {
			return nil
		}
		for i := range cur {
			if i < len(base) {
				found = append(found, newlyRequired(base[i], cur[i], fmt.Sprintf("%s/%d", path, i))...)
			}
		}
	}
	return found
}

func pathOrRoot(path string) string {
	if path == "" {
		return "/"
	}
	return path
}

// previousReleaseTag returns the baseline tag reachable from HEAD that does
// not point at HEAD's commit: the prod-rev<N> tag with the highest N, else the
// newest v* tag, else "".
func previousReleaseTag(root string) (string, error) {
	head, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	head = strings.TrimSpace(head)
	prodTags, err := gitOutput(root, "tag", "--merged", "HEAD", "--list", "prod-rev*")
	if err != nil {
		return "", err
	}
	type revTag struct {
		name string
		n    int
	}
	var revs []revTag
	for _, tag := range strings.Fields(prodTags) {
		n, err := strconv.Atoi(strings.TrimPrefix(tag, "prod-rev"))
		if err != nil || n < 0 {
			continue
		}
		revs = append(revs, revTag{tag, n})
	}
	sort.Slice(revs, func(i, j int) bool { return revs[i].n > revs[j].n })
	var ordered []string
	for _, r := range revs {
		ordered = append(ordered, r.name)
	}
	versionTags, err := gitOutput(root, "tag", "--merged", "HEAD", "--list", "v*", "--sort=-creatordate")
	if err != nil {
		return "", err
	}
	ordered = append(ordered, strings.Fields(versionTags)...)
	for _, tag := range ordered {
		commit, err := gitOutput(root, "rev-parse", tag+"^{commit}")
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(commit) != head {
			return tag, nil
		}
	}
	return "", nil
}

func gitOutput(root string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
