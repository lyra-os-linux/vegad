package version

// Version is overridden at build time via:
//   go build -ldflags "-X github.com/lyraos/vegad/internal/version.Version=x.y.z"
var Version = "2.0.0"
