package distro

import "errors"

// ErrUnsupported is returned by backend methods that model a capability the
// active distro simply doesn't have (e.g. mirror ranking on openSUSE,
// AUR-equivalent lookups when Provider.Community() is nil upstream).
var ErrUnsupported = errors.New("operação não suportada nesta distribuição")
