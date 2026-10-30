// Package line — две находки разных линтеров на одной строке (uniq-by-line).
package line

import "os/exec"

// List запускает команду без контекста и без проверки ошибки.
func List() {
	exec.Command("ls").Run()
}
