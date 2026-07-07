package version

// Version is overridden at build time via:
//   go build -ldflags "-X github.com/lyraos/vegad/internal/version.Version=x.y.z"
var Version = "0.1.0-dev"
