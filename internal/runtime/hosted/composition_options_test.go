package hosted

import (
	"context"
	"testing"
)

func TestOpen_leaves_episode_service_disabled_by_default(t *testing.T) {
	// Given
	events := []string{}
	request := testBuildRequest(t, &events, "")
	request.config.EnableEpisodeWriteback = false

	// When
	runtime, err := open(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := runtime.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})

	// Then
	if runtime.Dependencies.Runtime.Episodes != nil {
		t.Fatal("episode service was enabled without explicit configuration")
	}
	for _, event := range events {
		if event == "episode.new" {
			t.Fatal("episode service constructor ran while writeback was disabled")
		}
	}
}
