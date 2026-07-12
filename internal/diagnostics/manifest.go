package diagnostics

import "time"

// ManifestSchemaVersion identifies the shape of manifest.json below. It
// follows the same "<entity>.v<major>" convention as the wire contracts
// under internal/contracts/v1 (e.g. "capabilities.v1"), but is scoped to
// this local, offline bundle format only -- it is never sent over the
// wire and is not part of the hosted API/MCP contract surface.
const ManifestSchemaVersion = "diagnostics_bundle_manifest.v1"

// bundleFileNames lists every file the archive contains, in fixed,
// deterministic order. staticReportFile and liveReportFile are always
// present in this slice's logical position, but liveReportFile is only
// actually written when Input.Live is non-nil (see Build).
const (
	manifestFile      = "manifest.json"
	staticReportFile  = "doctor-static.json"
	liveReportFile    = "doctor-live.json"
	interpretationDoc = "README.md"
)

// manifest is the schema-versioned index describing the bundle's contents.
// It carries build/runtime identity and a fixed file list so a reader (or
// an automated scanner) knows what to expect without inspecting every
// entry first.
type manifest struct {
	SchemaVersion string    `json:"schema_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	Identity      Identity  `json:"identity"`
	Files         []string  `json:"files"`
	Status        string    `json:"status"`
	HasLiveReport bool      `json:"has_live_report"`
}

// buildManifest assembles the manifest for the given input, generation
// time, and whether a live report is included.
func buildManifest(input Input, generatedAt time.Time) manifest {
	files := []string{manifestFile, staticReportFile}
	if input.Live != nil {
		files = append(files, liveReportFile)
	}
	files = append(files, interpretationDoc)
	return manifest{
		SchemaVersion: ManifestSchemaVersion,
		GeneratedAt:   generatedAt,
		Identity:      input.Identity,
		Files:         files,
		Status:        input.Static.Status,
		HasLiveReport: input.Live != nil,
	}
}
