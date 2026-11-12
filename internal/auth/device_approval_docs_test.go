package auth

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

var mermaidBlockPattern = regexp.MustCompile("(?s)```mermaid\n(.*?)```")

func readDocFile(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func mermaidBlocks(text string) []string {
	var blocks []string
	for _, match := range mermaidBlockPattern.FindAllStringSubmatch(text, -1) {
		blocks = append(blocks, match[1])
	}
	return blocks
}

func TestDeviceApprovalPageHoldsTheSourceDiagrams(t *testing.T) {
	page := mermaidBlocks(readDocFile(t, "../../docs/device-approval.md"))
	source := mermaidBlocks(readDocFile(t, "device-approval-architecture.md"))
	if len(source) != 2 {
		t.Fatalf("the diagram source holds %d diagrams, want 2", len(source))
	}
	if len(page) != len(source) {
		t.Fatalf("the page holds %d diagrams, the source %d", len(page), len(source))
	}
	for i := range source {
		if page[i] != source[i] {
			t.Fatalf("diagram %d differs between the page and the source", i+1)
		}
	}
}

// tableRows returns the rows of the table whose header row starts with firstHeader, each cut to its
// first columns cells and trimmed.
func tableRows(text, firstHeader string, columns int) [][]string {
	lines := strings.Split(text, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "| "+firstHeader+" ") {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}
	var rows [][]string
	for i := start + 2; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
		cells := strings.Split(lines[i], "|")
		cells = cells[1 : len(cells)-1]
		row := make([]string, 0, columns)
		for _, cell := range cells[:columns] {
			row = append(row, strings.TrimSpace(cell))
		}
		rows = append(rows, row)
	}
	return rows
}

func TestDeviceApprovalPageHoldsTheSourceTables(t *testing.T) {
	page := readDocFile(t, "../../docs/device-approval.md")
	source := readDocFile(t, "device-approval-architecture.md")
	for _, table := range []struct {
		header  string
		columns int
		count   int
	}{{"Token", 5, 10}, {"Key", 3, 3}, {"Setting", 5, 5}} {
		pageRows := tableRows(page, table.header, table.columns)
		sourceRows := tableRows(source, table.header, table.columns)
		if len(sourceRows) != table.count {
			t.Fatalf("%s table: the source holds %d rows, want %d", table.header, len(sourceRows), table.count)
		}
		if len(pageRows) != len(sourceRows) {
			t.Fatalf("%s table: the page holds %d rows, the source %d", table.header, len(pageRows), len(sourceRows))
		}
		for i := range sourceRows {
			if strings.Join(pageRows[i], "|") != strings.Join(sourceRows[i], "|") {
				t.Fatalf("%s table row %d differs between the page and the source", table.header, i+1)
			}
		}
	}
}

func TestDeviceApprovalPageNamesNoTrackerIDRulingOrAgent(t *testing.T) {
	forbidden := regexp.MustCompile(`CHAOS-\d|\bD\d{4}\b|\b(?:acp|cag|fcsup|fcdev|lane)\b|#\d{3,}`)
	page := readDocFile(t, "../../docs/device-approval.md")
	if hit := forbidden.FindString(page); hit != "" {
		t.Fatalf("the public page names %q", hit)
	}
}

func TestDeviceApprovalEntryPointsPointAtTheSource(t *testing.T) {
	if !strings.Contains(readDocFile(t, "web_assertion.go"), "device-approval-architecture.md") {
		t.Fatal("web_assertion.go has no pointer to the diagram source")
	}
	if !strings.Contains(readDocFile(t, "../api/device_routes.go"), "device-approval-architecture.md") {
		t.Fatal("device_routes.go has no pointer to the diagram source")
	}
}
