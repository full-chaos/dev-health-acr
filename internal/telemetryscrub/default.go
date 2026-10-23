package telemetryscrub

import (
	"io"
	"log/slog"
)

// InstallDefault makes package-level slog calls reach w through the scrubber.
// It installs no exporting handler, so a library reading slog.Default gets
// no telemetry pipeline.
func InstallDefault(w io.Writer, level slog.Level) {
	slog.SetDefault(slog.New(Handler(slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level}))))
}
