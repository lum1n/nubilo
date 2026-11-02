package version

// String is the nubilo release label shown by CLI and UI. Release builds
// set it with -ldflags "-X nubilo/internal/version.String=0.8.0"; the
// updater compares it with published releases.
var String = "0.7.0-phase8"
