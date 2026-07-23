package dbusserver

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// UsersService backs org.lyraos.Vega1.Users: account creation/removal,
// password, wheel (admin) membership and simplified
// sudo rules.
type UsersService struct {
	activity *Activity
	conn     *dbus.Conn
}

type UserInfo struct {
	Username string
	FullName string
	Groups   []string
	IsAdmin  bool
}

func (u *UsersService) ListUsers() ([]UserInfo, *dbus.Error) {
	u.activity.Touch()
	rows, err := readUserInfos()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return rows, nil
}

func (u *UsersService) ListGroups() ([]string, *dbus.Error) {
	u.activity.Touch()
	groups, err := readGroupNames()
	if err != nil {
		return nil, dbus.MakeFailedError(err)
	}
	return groups, nil
}

func (u *UsersService) CreateUser(sender dbus.Sender, username, fullName, password string, groups []string, photo []byte, isAdmin bool) *dbus.Error {
	u.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.users.manage"); err != nil {
		return err
	}
	if err := validateUsername(username); err != nil {
		return dbus.MakeFailedError(err)
	}
	if err := validateUserDetails(fullName, password, photo); err != nil {
		return dbus.MakeFailedError(err)
	}
	available, err := groupNameSet()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	selected := make([]string, 0, len(groups)+1)
	seen := map[string]bool{}
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" || seen[group] {
			continue
		}
		if !available[group] {
			return dbus.MakeFailedError(fmt.Errorf("grupo inexistente: %s", group))
		}
		seen[group] = true
		selected = append(selected, group)
	}
	if isAdmin {
		if !available["wheel"] {
			return dbus.MakeFailedError(fmt.Errorf("grupo administrativo wheel não existe"))
		}
		if !seen["wheel"] {
			selected = append(selected, "wheel")
		}
	}
	args := []string{"-m", "-s", "/bin/bash", "-c", fullName}
	if len(selected) > 0 {
		args = append(args, "-G", strings.Join(selected, ","))
	}
	args = append(args, username)
	if out, err := exec.Command("useradd", args...).CombinedOutput(); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("useradd: %w — %s", err, strings.TrimSpace(string(out))))
	}
	rollback := true
	defer func() {
		if rollback {
			_ = exec.Command("userdel", "-r", username).Run()
		}
	}()
	chpasswd := exec.Command("chpasswd")
	chpasswd.Stdin = strings.NewReader(username + ":" + password + "\n")
	if out, err := chpasswd.CombinedOutput(); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("chpasswd: %w — %s", err, strings.TrimSpace(string(out))))
	}
	if len(photo) > 0 {
		if err := installUserPhoto(username, photo); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	rollback = false
	return nil
}

func (u *UsersService) UpdateUser(sender dbus.Sender, username, fullName, password string, groups []string, photo []byte, isAdmin bool) *dbus.Error {
	u.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.users.manage"); err != nil {
		return err
	}
	if err := validateUsername(username); err != nil {
		return dbus.MakeFailedError(err)
	}
	if username == "root" {
		return dbus.MakeFailedError(fmt.Errorf("a conta root não pode ser editada"))
	}
	if strings.ContainsAny(fullName, ":\r\n") || strings.TrimSpace(fullName) == "" {
		return dbus.MakeFailedError(fmt.Errorf("nome completo inválido"))
	}
	if password != "" {
		if err := validatePassword(password); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	if len(photo) > 5*1024*1024 || len(photo) > 0 && !isSupportedImage(photo) {
		return dbus.MakeFailedError(fmt.Errorf("a foto deve ser PNG ou JPEG e ter no máximo 5 MB"))
	}
	available, err := groupNameSet()
	if err != nil {
		return dbus.MakeFailedError(err)
	}
	selected := make([]string, 0, len(groups)+1)
	seen := map[string]bool{}
	for _, group := range groups {
		group = strings.TrimSpace(group)
		if group == "" || seen[group] {
			continue
		}
		if !available[group] {
			return dbus.MakeFailedError(fmt.Errorf("grupo inexistente: %s", group))
		}
		seen[group] = true
		selected = append(selected, group)
	}
	if isAdmin && !seen["wheel"] {
		selected = append(selected, "wheel")
	}
	if out, err := exec.Command("usermod", "-c", fullName, "-G", strings.Join(selected, ","), username).CombinedOutput(); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("usermod: %w — %s", err, strings.TrimSpace(string(out))))
	}
	if password != "" {
		cmd := exec.Command("chpasswd")
		cmd.Stdin = strings.NewReader(username + ":" + password + "\n")
		if out, err := cmd.CombinedOutput(); err != nil {
			return dbus.MakeFailedError(fmt.Errorf("chpasswd: %w — %s", err, strings.TrimSpace(string(out))))
		}
	}
	if len(photo) > 0 {
		if err := installUserPhoto(username, photo); err != nil {
			return dbus.MakeFailedError(err)
		}
	}
	return nil
}

func validateUserDetails(fullName, password string, photo []byte) error {
	if strings.TrimSpace(fullName) == "" || strings.ContainsAny(fullName, ":\r\n") {
		return fmt.Errorf("nome completo inválido")
	}
	if err := validatePassword(password); err != nil {
		return err
	}
	if len(photo) > 5*1024*1024 {
		return fmt.Errorf("a foto deve ter no máximo 5 MB")
	}
	if len(photo) > 0 && !isSupportedImage(photo) {
		return fmt.Errorf("a foto deve ser PNG ou JPEG")
	}
	return nil
}

func validatePassword(password string) error {
	if len([]rune(password)) < 8 {
		return fmt.Errorf("a senha deve ter pelo menos 8 caracteres")
	}
	if strings.ContainsAny(password, "\r\n:") {
		return fmt.Errorf("a senha contém caracteres inválidos")
	}
	return nil
}

func isSupportedImage(photo []byte) bool {
	return len(photo) >= 8 && string(photo[:8]) == "\x89PNG\r\n\x1a\n" ||
		len(photo) >= 3 && photo[0] == 0xff && photo[1] == 0xd8 && photo[2] == 0xff
}

func installUserPhoto(username string, photo []byte) error {
	const iconsDir = "/var/lib/AccountsService/icons"
	const usersDir = "/var/lib/AccountsService/users"
	if err := os.MkdirAll(iconsDir, 0755); err != nil {
		return fmt.Errorf("criando diretório de fotos: %w", err)
	}
	if err := os.MkdirAll(usersDir, 0755); err != nil {
		return fmt.Errorf("criando configuração da conta: %w", err)
	}
	iconPath := filepath.Join(iconsDir, username)
	if err := os.WriteFile(iconPath, photo, 0644); err != nil {
		return fmt.Errorf("salvando foto: %w", err)
	}
	config := []byte("[User]\nIcon=" + iconPath + "\n")
	if err := os.WriteFile(filepath.Join(usersDir, username), config, 0644); err != nil {
		_ = os.Remove(iconPath)
		return fmt.Errorf("configurando foto: %w", err)
	}
	return nil
}

func readGroupNames() ([]string, error) {
	set, err := groupNameSet()
	if err != nil {
		return nil, err
	}
	groups := make([]string, 0, len(set))
	for group := range set {
		groups = append(groups, group)
	}
	sort.Strings(groups)
	return groups, nil
}

func groupNameSet() (map[string]bool, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return nil, err
	}
	defer f.Close()
	groups := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.SplitN(scanner.Text(), ":", 2)
		if len(fields) == 2 && fields[0] != "" {
			groups[fields[0]] = true
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func (u *UsersService) RemoveUser(sender dbus.Sender, username string) *dbus.Error {
	u.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.users.manage"); err != nil {
		return err
	}
	if err := validateUsername(username); err != nil {
		return dbus.MakeFailedError(err)
	}
	if username == "root" {
		return dbus.MakeFailedError(fmt.Errorf("não é permitido remover o usuário root"))
	}
	if err := exec.Command("userdel", "-r", username).Run(); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("userdel: %w", err))
	}
	_ = os.Remove(filepath.Join("/var/lib/AccountsService/icons", username))
	_ = os.Remove(filepath.Join("/var/lib/AccountsService/users", username))
	return nil
}

func (u *UsersService) SetAdmin(sender dbus.Sender, username string, isAdmin bool) *dbus.Error {
	u.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.users.manage"); err != nil {
		return err
	}
	if err := validateUsername(username); err != nil {
		return dbus.MakeFailedError(err)
	}
	if username == "root" {
		return nil
	}

	var cmd *exec.Cmd
	if isAdmin {
		cmd = exec.Command("usermod", "-aG", "wheel", username)
	} else {
		cmd = exec.Command("gpasswd", "-d", username, "wheel")
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if !isNonFatalGroupRemoval(err, out) {
			return dbus.MakeFailedError(fmt.Errorf("%s: %w — %s", cmd.Path, err, strings.TrimSpace(string(out))))
		}
	}
	return nil
}

var usernameRe = regexp.MustCompile(`^[a-z_][a-z0-9_-]*[$]?$`)

func validateUsername(username string) error {
	username = strings.TrimSpace(username)
	if username == "" {
		return fmt.Errorf("nome de usuário vazio")
	}
	if !usernameRe.MatchString(username) {
		return fmt.Errorf("nome de usuário inválido: %s", username)
	}
	return nil
}

func readUserInfos() ([]UserInfo, error) {
	admins, err := wheelMembers()
	if err != nil {
		return nil, err
	}

	f, err := os.Open("/etc/passwd")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var rows []UserInfo
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 7 {
			continue
		}
		uid, err := strconv.Atoi(fields[2])
		if err != nil {
			continue
		}
		username := fields[0]
		// nobody sits at a high UID (65534 on most distros) precisely so it
		// falls outside the normal 1000+ range and isn't mistaken for a
		// real account — exclude it by name instead of relying on that UID
		// convention, since it's not guaranteed across distros.
		if username == "nobody" {
			continue
		}
		if uid != 0 && uid < 1000 {
			continue
		}
		rows = append(rows, UserInfo{
			Username: username,
			FullName: strings.Split(fields[4], ",")[0],
			Groups:   userGroups(username),
			IsAdmin:  admins[username] || username == "root",
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Username < rows[j].Username })
	return rows, nil
}

func userGroups(username string) []string {
	out, err := exec.Command("id", "-nG", username).Output()
	if err != nil {
		return nil
	}
	groups := strings.Fields(strings.TrimSpace(string(out)))
	result := groups[:0]
	for _, group := range groups {
		if group != username && group != "wheel" {
			result = append(result, group)
		}
	}
	return result
}

func wheelMembers() (map[string]bool, error) {
	f, err := os.Open("/etc/group")
	if err != nil {
		return nil, err
	}
	defer f.Close()

	admins := map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "wheel:") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) < 4 {
			break
		}
		for _, member := range strings.Split(fields[3], ",") {
			member = strings.TrimSpace(member)
			if member != "" {
				admins[member] = true
			}
		}
		break
	}
	return admins, scanner.Err()
}

func isNonFatalGroupRemoval(err error, out []byte) bool {
	text := strings.ToLower(string(out))
	return strings.Contains(text, "not a member") || strings.Contains(text, "usuario") || strings.Contains(text, "não é membro")
}
