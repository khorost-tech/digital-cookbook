// This file was automatically generated. Do not edit by hand.

package gen

import "os"

// CleanAuto — в файле с нестандартным заголовком.
func CleanAuto() {
	os.Remove("auto.tmp") // want errcheck // noise gosec
}
