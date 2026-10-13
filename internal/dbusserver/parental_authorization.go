package dbusserver

import "github.com/godbus/dbus/v5"

const parentalEnrollmentPolkitAction = "org.lyraos.vega.parental.enroll"

// parentalEnrollmentAuthorization binds both the interactive prompt and every
// later noninteractive recheck to the original D-Bus sender. It intentionally
// carries no long-lived authorization state and is not exported as a method.
func parentalEnrollmentAuthorization(sender dbus.Sender) (func() (bool, error), func() error) {
	authorize := func() (bool, error) {
		if err := requirePolkit(sender, parentalEnrollmentPolkitAction); err != nil {
			return false, err
		}
		return true, nil
	}
	revalidate := func() error {
		if err := checkPolkit(sender, parentalEnrollmentPolkitAction, false); err != nil {
			return err
		}
		return nil
	}
	return authorize, revalidate
}
