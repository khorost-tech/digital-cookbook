// Package cases — формы //nolint. Метка `// case` называет случай, `// want-next`
// размечает дефект строки ниже: на строке с директивой метку ставить нельзя.
package cases

import (
	"os"
	"os/exec"
)

// Bare — голая директива без имени линтера и без причины.
func Bare() {
	// case bare
	// want-next errcheck
	os.Remove("a.tmp") //nolint
}

// NoReason — имя линтера есть, причины нет.
func NoReason() {
	// case no-reason
	// want-next errcheck
	os.Remove("b.tmp") //nolint:errcheck
}

// WithReason — имя линтера и причина.
func WithReason() {
	// case with-reason
	// want-next errcheck
	os.Remove("c.tmp") //nolint:errcheck // временный файл; его отсутствие — норма
}

// WrongLinter — директива называет линтер, которого на строке нет.
func WrongLinter() {
	// case wrong-linter
	// want-next errcheck
	os.Remove("d.tmp") //nolint:noctx // вызов без контекста здесь допустим
}

// TwoBare — две находки на строке, голая директива прячет обе.
func TwoBare() {
	// case two-bare
	// want-next errcheck noctx
	exec.Command("true").Run() //nolint
}

// TwoSpecific — две находки на строке, директива называет одну.
func TwoSpecific() {
	// case two-specific
	// want-next errcheck noctx
	exec.Command("true").Run() //nolint:errcheck // код выхода не важен
}
