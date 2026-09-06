package dbusserver

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/godbus/dbus/v5"
	"github.com/lyraos/vegad/internal/distro"
)

// KernelService backs org.lyraos.Vega1.Kernel: switches between kernel
// variants, regenerating the bootloader config. Must never
// remove the running kernel or leave the system with zero kernels. Kernel
// package naming and boot-artifact regeneration are distro-specific
// (distro.KernelBackend); GRUB vs systemd-boot detection below is not — both
// bootloaders show up on either distro.
type KernelService struct {
	activity *Activity
	conn     *dbus.Conn
	provider distro.Provider
	nextTxID atomic.Uint32
}

type BootStatus struct {
	Loader       string
	DefaultEntry string
	Timeout      uint32
	Cmdline      string
}

func (k *KernelService) ListInstalled() ([]string, *dbus.Error) {
	k.activity.Touch()
	kernels, err := k.provider.Kernel().ListInstalled()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return kernels, nil
}

// AvailablePackages lists every kernel package installable on the active
// distro (not just what's already installed), so the UI's "install a
// kernel" picker doesn't have to hardcode package names.
func (k *KernelService) AvailablePackages() ([]string, *dbus.Error) {
	k.activity.Touch()
	return k.provider.Kernel().AvailablePackages(), nil
}

func (k *KernelService) Install(sender dbus.Sender, kernel string) (uint32, *dbus.Error) {
	k.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.kernel.switch"); err != nil {
		return 0, err
	}
	kb := k.provider.Kernel()
	if !kb.IsSupportedPackage(kernel) {
		return 0, dbus.MakeFailedError(fmt.Errorf("kernel inválido: %s", kernel))
	}

	done, ok := k.activity.begin()
	if !ok {
		return 0, dbus.MakeFailedError(fmt.Errorf("daemon encerrando; repita a chamada"))
	}
	txID := k.nextTxID.Add(1)
	go func() {
		defer done()
		err := withSnapshots("Instalação de kernel: "+kernel, func() error {
			return kb.Install(kernel, func(uint32, string) {})
		})
		if err == nil {
			err = kb.RebuildBootArtifacts()
		}
		if err != nil {
			logKernelError("install", kernel, err)
		}
	}()
	return txID, nil
}

func (k *KernelService) Remove(sender dbus.Sender, kernel string) *dbus.Error {
	k.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.kernel.switch"); err != nil {
		return err
	}
	kb := k.provider.Kernel()
	if !kb.IsSupportedPackage(kernel) {
		return dbus.MakeFailedError(fmt.Errorf("kernel inválido: %s", kernel))
	}
	if kb.RunningKernelMatches(kernel) {
		return dbus.MakeFailedError(fmt.Errorf("não é permitido remover o kernel em execução (%s)", kernel))
	}

	installed, err := kb.ListInstalled()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	installedSet := map[string]bool{}
	for _, name := range installed {
		installedSet[name] = true
	}
	if len(installed) <= 1 && installedSet[kernel] {
		return dbus.MakeFailedError(fmt.Errorf("não é permitido deixar o sistema sem kernel instalado"))
	}

	if !installedSet[kernel] {
		return nil
	}

	if err := withSnapshots("Remoção de kernel: "+kernel, func() error {
		return kb.Remove(kernel)
	}); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := kb.RebuildBootArtifacts(); err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func (k *KernelService) BootStatus() (BootStatus, *dbus.Error) {
	k.activity.Touch()
	status := BootStatus{Loader: detectBootloader(k.provider.Kernel().GrubConfigPath()), Timeout: 5}
	switch status.Loader {
	case "grub":
		status.DefaultEntry = grubDefault()
		status.Timeout = grubTimeout()
		status.Cmdline = grubCmdline()
	case "systemd-boot":
		status.DefaultEntry, status.Timeout = systemdBootLoaderConf()
		status.Cmdline = systemdBootCmdline()
	default:
		status.Loader = "não detectado"
	}
	return status, nil
}

func (k *KernelService) ListBootEntries() ([]string, *dbus.Error) {
	k.activity.Touch()
	grubConfigPath := k.provider.Kernel().GrubConfigPath()
	switch detectBootloader(grubConfigPath) {
	case "systemd-boot":
		matches, err := filepath.Glob("/boot/loader/entries/*.conf")
		if err != nil {
			return nil, dbus.MakeFailedError(err)
		}
		entries := make([]string, 0, len(matches))
		for _, path := range matches {
			entries = append(entries, strings.TrimSuffix(filepath.Base(path), ".conf"))
		}
		return entries, nil
	case "grub":
		if commandAvailable("awk") {
			out, err := runCommandOutput("awk", "-F'", "/^menuentry / {print $2}", grubConfigPath)
			if err == nil && strings.TrimSpace(out) != "" {
				return nonEmptyLines(out), nil
			}
		}
		return []string{"saved", "0"}, nil
	default:
		return []string{}, nil
	}
}

func (k *KernelService) ApplyBootConfig(sender dbus.Sender, defaultEntry string, timeout uint32, cmdline string) *dbus.Error {
	k.activity.Touch()
	if err := validateBootValues(defaultEntry, cmdline); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := requirePolkit(sender, "org.lyraos.vega.kernel.switch"); err != nil {
		return err
	}
	kb := k.provider.Kernel()
	err := withSnapshots("Configuração de bootloader", func() error {
		switch detectBootloader(kb.GrubConfigPath()) {
		case "grub":
			return applyGrubBootConfig(defaultEntry, timeout, cmdline, kb)
		case "systemd-boot":
			return applySystemdBootConfig(defaultEntry, timeout, cmdline)
		default:
			return fmt.Errorf("bootloader não detectado")
		}
	})
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	return nil
}

func detectBootloader(grubConfigPath string) string {
	if _, err := os.Stat("/boot/loader/loader.conf"); err == nil {
		return "systemd-boot"
	}
	if _, err := os.Stat(grubConfigPath); err == nil {
		return "grub"
	}
	if commandAvailable("bootctl") {
		if out, err := runCommandOutput("bootctl", "is-installed"); err == nil && strings.Contains(out, "yes") {
			return "systemd-boot"
		}
	}
	return ""
}

func grubDefault() string {
	return grubSetting("GRUB_DEFAULT", "saved")
}

func grubTimeout() uint32 {
	value := grubSetting("GRUB_TIMEOUT", "5")
	var timeout uint64
	timeout, _ = strconv.ParseUint(value, 10, 32)
	return uint32(timeout)
}

func grubCmdline() string {
	return grubSetting("GRUB_CMDLINE_LINUX_DEFAULT", "")
}

func grubSetting(key, fallback string) string {
	data, err := os.ReadFile("/etc/default/grub")
	if err != nil {
		return fallback
	}
	re := regexp.MustCompile(`^` + regexp.QuoteMeta(key) + `=(.*)$`)
	for _, line := range strings.Split(string(data), "\n") {
		m := re.FindStringSubmatch(strings.TrimSpace(line))
		if len(m) == 2 {
			return unquoteShell(strings.TrimSpace(m[1]))
		}
	}
	return fallback
}

func applyGrubBootConfig(defaultEntry string, timeout uint32, cmdline string, kb distro.KernelBackend) error {
	return applyGrubBootConfigAt("/etc/default/grub", defaultEntry, timeout, cmdline, kb.RebuildBootArtifacts)
}

func applyGrubBootConfigAt(path, defaultEntry string, timeout uint32, cmdline string, rebuild func() error) error {
	if err := validateBootValues(defaultEntry, cmdline); err != nil {
		return err
	}
	previous, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := rewriteKeyValueFile(path, map[string]string{
		"GRUB_DEFAULT":               quoteShell(defaultEntry),
		"GRUB_TIMEOUT":               fmt.Sprintf("%d", timeout),
		"GRUB_CMDLINE_LINUX_DEFAULT": quoteShell(cmdline),
		"GRUB_SAVEDEFAULT":           "true",
		"GRUB_DISABLE_SUBMENU":       "y",
	}); err != nil {
		return err
	}
	if err := rebuild(); err != nil {
		if restoreErr := writeBootConfigAtomically(path, previous); restoreErr != nil {
			return errors.Join(err, fmt.Errorf("recuperar configuração anterior do GRUB: %w", restoreErr))
		}
		return err
	}
	return nil
}

func systemdBootLoaderConf() (string, uint32) {
	data, err := os.ReadFile("/boot/loader/loader.conf")
	if err != nil {
		return "", 5
	}
	defaultEntry := ""
	timeout := uint32(5)
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 2 {
			continue
		}
		switch fields[0] {
		case "default":
			defaultEntry = fields[1]
		case "timeout":
			value, _ := strconv.ParseUint(fields[1], 10, 32)
			timeout = uint32(value)
		}
	}
	return strings.TrimSuffix(defaultEntry, ".conf"), timeout
}

func systemdBootCmdline() string {
	if value, err := readTrimmedFile("/etc/kernel/cmdline"); err == nil {
		return value
	}
	matches, err := filepath.Glob("/boot/loader/entries/*.conf")
	if err != nil || len(matches) == 0 {
		return ""
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "options ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "options "))
		}
	}
	return ""
}

func applySystemdBootConfig(defaultEntry string, timeout uint32, cmdline string) error {
	if err := validateBootValues(defaultEntry, cmdline); err != nil {
		return err
	}
	if defaultEntry != "" && !strings.HasSuffix(defaultEntry, ".conf") {
		defaultEntry += ".conf"
	}
	if err := rewriteConfigFile("/boot/loader/loader.conf", " ", map[string]string{
		"default": defaultEntry,
		"timeout": fmt.Sprintf("%d", timeout),
	}); err != nil {
		return err
	}
	if strings.TrimSpace(cmdline) != "" {
		if err := os.WriteFile("/etc/kernel/cmdline", []byte(strings.TrimSpace(cmdline)+"\n"), 0644); err != nil {
			return err
		}
	}
	if commandAvailable("bootctl") {
		_ = runCommand("bootctl", "update")
	}
	return nil
}

func rewriteKeyValueFile(path string, values map[string]string) error {
	return rewriteConfigFile(path, "=", values)
}

func rewriteConfigFile(path, separator string, values map[string]string) error {
	for key, value := range values {
		if strings.ContainsAny(key+value, "\x00\r\n") {
			return fmt.Errorf("configuração de boot deve conter valores de uma única linha")
		}
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	lines := strings.Split(string(data), "\n")
	seen := map[string]bool{}
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		for key, value := range values {
			if strings.HasPrefix(trimmed, key+"=") || strings.HasPrefix(trimmed, key+" ") || strings.HasPrefix(trimmed, key+"\t") {
				lines[i] = key + separator + value
				seen[key] = true
			}
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if !seen[key] {
			lines = append(lines, key+separator+values[key])
		}
	}
	return writeBootConfigAtomically(path, []byte(strings.TrimSpace(strings.Join(lines, "\n"))+"\n"))
}

func quoteShell(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`").Replace(value) + `"`
}

// Decode quotes and escapes without ever asking a shell to evaluate the file.
func unquoteShell(value string) string {
	var out strings.Builder
	var quote byte
	for i := 0; i < len(value); i++ {
		c := value[i]
		if c == '\\' && quote != '\'' && i+1 < len(value) {
			next := value[i+1]
			if quote == 0 || strings.ContainsRune("\\\"$`", rune(next)) {
				out.WriteByte(next)
				i++
				continue
			}
		}
		if quote == 0 && (c == '\'' || c == '"') {
			quote = c
		} else if quote != 0 && c == quote {
			quote = 0
		} else {
			out.WriteByte(c)
		}
	}
	return out.String()
}

func validateBootValues(values ...string) error {
	for _, value := range values {
		if strings.ContainsAny(value, "\x00\r\n") {
			return fmt.Errorf("parâmetros de boot não podem conter NUL ou quebras de linha")
		}
	}
	return nil
}

func writeBootConfigAtomically(path string, data []byte) error {
	mode := os.FileMode(0o644)
	info, err := os.Lstat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("configuração de boot não é um arquivo regular: %s", path)
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".vega-boot-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Chmod(mode); err != nil {
		return err
	}
	if info != nil {
		if stat, ok := info.Sys().(*syscall.Stat_t); ok {
			if err := file.Chown(int(stat.Uid), int(stat.Gid)); err != nil {
				return err
			}
		}
		if err := copyBootConfigXattrs(path, file.Name()); err != nil {
			return err
		}
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), path); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func copyBootConfigXattrs(source, destination string) error {
	size, err := syscall.Listxattr(source, nil)
	if errors.Is(err, syscall.ENOTSUP) {
		return nil
	}
	if err != nil {
		return err
	}
	names := make([]byte, size)
	if _, err := syscall.Listxattr(source, names); err != nil {
		return err
	}
	for _, name := range strings.Split(string(names), "\x00") {
		if name == "" {
			continue
		}
		size, err := syscall.Getxattr(source, name, nil)
		if err != nil {
			return err
		}
		value := make([]byte, size)
		if _, err := syscall.Getxattr(source, name, value); err != nil {
			return err
		}
		if err := syscall.Setxattr(destination, name, value, 0); err != nil {
			return err
		}
	}
	return nil
}

func logKernelError(action, kernel string, err error) {
	fmt.Printf("vegad: kernel %s %s failed: %v\n", action, kernel, err)
}
