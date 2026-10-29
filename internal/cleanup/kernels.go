package cleanup

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type kernelPlan struct {
	packages []string
	bytes    uint64
}

var (
	purgeRemoved  = regexp.MustCompile(`^The following (?:\d+ packages are|package is) going to be REMOVED:$`)
	purgeReleased = regexp.MustCompile(`([\d.]+)\s*(B|KiB|MiB|GiB|TiB)\s+released by packages that will be removed`)
)

// parsePurgeKernels reads "zypper purge-kernels --dry-run": the packages
// after "going to be REMOVED:" up to the blank line, and the size the
// summary says they release.
func parsePurgeKernels(out []byte) (kernelPlan, error) {
	var plan kernelPlan
	scanner := bufio.NewScanner(bytes.NewReader(out))
	inList := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case purgeRemoved.MatchString(strings.TrimSpace(line)):
			inList = true
		case inList && strings.TrimSpace(line) == "":
			inList = false
		case inList:
			plan.packages = append(plan.packages, strings.Fields(line)...)
		}
		if match := purgeReleased.FindStringSubmatch(line); match != nil {
			size, err := parseZypperSize(match[1], match[2])
			if err != nil {
				return plan, err
			}
			plan.bytes = size
		}
	}
	if len(plan.packages) > 0 && plan.bytes == 0 {
		return plan, fmt.Errorf("purge-kernels não informou o espaço liberado")
	}
	return plan, scanner.Err()
}

func parseZypperSize(number, unit string) (uint64, error) {
	value, err := strconv.ParseFloat(number, 64)
	if err != nil {
		return 0, err
	}
	scale := map[string]float64{"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40}[unit]
	return uint64(value * scale), nil
}

// runningKernel is the release of the running kernel without the flavor:
// "6.12.0-160100.7-default" gives ("6.12.0-160100.7", "default").
func runningKernel(env *Env) (release, flavor string, err error) {
	data, err := os.ReadFile(env.path("/proc/sys/kernel/osrelease"))
	if err != nil {
		return "", "", err
	}
	full := strings.TrimSpace(string(data))
	i := strings.LastIndex(full, "-")
	if i <= 0 {
		return "", "", fmt.Errorf("release do kernel inesperado: %q", full)
	}
	return full[:i], full[i+1:], nil
}

// installedVersions lists the version-release of every installed copy of pkg.
func installedVersions(ctx context.Context, env *Env, pkg string) ([]string, error) {
	out, code, err := env.Run(ctx, "", "rpm", "-q", "--qf", "%{VERSION}-%{RELEASE}\\n", pkg)
	if err != nil {
		return nil, err
	}
	if code != 0 {
		return nil, nil
	}
	return strings.Fields(string(out)), nil
}

// isRunning matches an RPM version-release against the running release:
// 6.12.0-160100.7.2 is the package of 6.12.0-160100.7-default.
func isRunning(versionRelease, running string) bool {
	return versionRelease == running || strings.HasPrefix(versionRelease, running+".")
}

// protectedKernels refuses a plan that would take the running kernel or the
// newest one, whatever multiversion.kernels says. Packages without a version
// in the list (kernel-default-extra) are checked by what is installed.
func protectedKernels(ctx context.Context, env *Env, packages []string) (bool, error) {
	running, flavor, err := runningKernel(env)
	if err != nil {
		return true, err
	}
	installed, err := installedVersions(ctx, env, "kernel-"+flavor)
	if err != nil || len(installed) == 0 {
		return true, err
	}
	newest := installed[0]
	for _, version := range installed[1:] {
		if compareVersions(version, newest) > 0 {
			newest = version
		}
	}
	versionOf := regexp.MustCompile(`^(kernel-[a-z0-9_-]+?)-(\d[^-]*-[^-]+)$`)
	for _, pkg := range packages {
		versions := []string{}
		if match := versionOf.FindStringSubmatch(pkg); match != nil {
			versions = append(versions, match[2])
		} else {
			list, err := installedVersions(ctx, env, pkg)
			if err != nil {
				return true, err
			}
			versions = list
		}
		for _, version := range versions {
			if isRunning(version, running) || version == newest {
				return true, nil
			}
		}
	}
	return false, nil
}

// compareVersions orders RPM version-release strings by their numeric and
// alphabetic segments, as rpmvercmp does for the kernel's dotted numbers.
func compareVersions(a, b string) int {
	split := func(s string) []string {
		return regexp.MustCompile(`\d+|[A-Za-z]+`).FindAllString(s, -1)
	}
	left, right := split(a), split(b)
	for i := 0; i < len(left) && i < len(right); i++ {
		x, errX := strconv.ParseUint(left[i], 10, 64)
		y, errY := strconv.ParseUint(right[i], 10, 64)
		switch {
		case errX == nil && errY == nil:
			if x != y {
				if x > y {
					return 1
				}
				return -1
			}
		case errX == nil:
			return 1
		case errY == nil:
			return -1
		default:
			if c := strings.Compare(left[i], right[i]); c != 0 {
				return c
			}
		}
	}
	return len(left) - len(right)
}

func measureKernels(ctx context.Context, env *Env) (measurement, error) {
	if reason := missingTool(env, "zypper", "zypper"); reason != "" {
		return measurement{reason: reason}, nil
	}
	out, code, err := env.Run(ctx, "", "zypper", "-n", "--no-refresh", "purge-kernels", "--dry-run")
	if err != nil {
		return measurement{}, err
	}
	if code == 7 {
		return measurement{reason: "busy"}, nil
	}
	if code != 0 {
		return measurement{}, fmt.Errorf("purge-kernels --dry-run: código %d", code)
	}
	plan, err := parsePurgeKernels(out)
	if err != nil {
		return measurement{}, err
	}
	m := measurement{bytes: plan.bytes, count: len(plan.packages), items: plan.packages}
	if len(plan.packages) > 0 {
		protected, err := protectedKernels(ctx, env, plan.packages)
		if err != nil || protected {
			m.reason = "protected"
		}
	}
	return m, nil
}

// cleanKernels runs purge-kernels inside a snapshot pair. The space comes
// back on disk only when the snapshots holding the old files are cleaned.
func cleanKernels(ctx context.Context, env *Env, _ measurement) error {
	return env.Snapshot("Vega: remover kernels antigos", func() error {
		_, err := runOK(ctx, env, "", "zypper", "-n", "--no-refresh", "purge-kernels")
		return err
	})
}
