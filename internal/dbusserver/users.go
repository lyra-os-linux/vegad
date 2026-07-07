package dbusserver

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// UsersService backs org.lyraos.Vega1.Users (PROMPT-VEGA.md §3.6): account
// creation/removal, password, wheel (admin) membership and simplified
// sudo rules.
type UsersService struct {
	activity *Activity
	conn     *dbus.Conn
}

type UserInfo struct {
	Username string
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

func (u *UsersService) CreateUser(sender dbus.Sender, username string, isAdmin bool) *dbus.Error {
	u.activity.Touch()
	if err := requirePolkit(sender, "org.lyraos.vega.users.manage"); err != nil {
		return err
	}
	if err := validateUsername(username); err != nil {
		return dbus.MakeFailedError(err)
	}
	args := []string{"-m", "-s", "/bin/bash"}
	if isAdmin {
		args = append(args, "-G", "wheel")
	}
	args = append(args, username)
	if err := exec.Command("useradd", args...).Run(); err != nil {
		return dbus.MakeFailedError(fmt.Errorf("useradd: %w", err))
	}
	return nil
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
		if uid != 0 && uid < 1000 {
			continue
		}
		username := fields[0]
		rows = append(rows, UserInfo{Username: username, IsAdmin: admins[username] || username == "root"})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].Username < rows[j].Username })
	return rows, nil
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
