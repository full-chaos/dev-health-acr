package evaldemo

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/full-chaos/dev-health-acr/internal/evalfixture"
)

func loadCorpus(config Config) (evalfixture.Corpus, error) {
	if config.Scenario != ScenarioCorruptHash {
		return evalfixture.VerifyCorpus(config.CorpusDir)
	}
	return corruptCorpus(config.CorpusDir)
}

func corruptCorpus(source string) (corpus evalfixture.Corpus, err error) {
	destination, err := os.MkdirTemp("", "acr-evaluation-corrupt-")
	if err != nil {
		return evalfixture.Corpus{}, fmt.Errorf("create temporary corpus: %w", err)
	}
	defer func() {
		if cleanupErr := os.RemoveAll(destination); cleanupErr != nil && err == nil {
			err = fmt.Errorf("remove temporary corpus: %w", cleanupErr)
		}
	}()
	if err := copyDirectory(source, destination); err != nil {
		return evalfixture.Corpus{}, err
	}
	file, err := os.OpenFile(filepath.Join(destination, "scenario.json"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		return evalfixture.Corpus{}, fmt.Errorf("open scenario fixture: %w", err)
	}
	if _, err := file.WriteString("\n"); err != nil {
		if closeErr := file.Close(); closeErr != nil {
			return evalfixture.Corpus{}, errors.Join(fmt.Errorf("corrupt scenario fixture: %w", err), fmt.Errorf("close scenario fixture: %w", closeErr))
		}
		return evalfixture.Corpus{}, fmt.Errorf("corrupt scenario fixture: %w", err)
	}
	if err := file.Close(); err != nil {
		return evalfixture.Corpus{}, fmt.Errorf("close scenario fixture: %w", err)
	}
	return evalfixture.VerifyCorpus(destination)
}

func copyDirectory(source, destination string) error {
	err := filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		return fmt.Errorf("copy corpus: %w", err)
	}
	return nil
}
