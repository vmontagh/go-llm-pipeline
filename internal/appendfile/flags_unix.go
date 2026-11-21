//go:build unix

package appendfile

import "syscall"

const extraOpenFlags = syscall.O_NONBLOCK | syscall.O_NOFOLLOW
