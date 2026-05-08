package version

// Version is the current release version. Overridden at build time via:
//
//	go build -ldflags="-X internal/version.Version=0.3.0"
var Version = "0.2.0"
