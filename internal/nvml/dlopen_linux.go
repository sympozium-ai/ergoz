package nvml

import "github.com/ebitengine/purego"

func dlopen(name string) (uintptr, error) {
	return purego.Dlopen(name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
}

func register(fptr interface{}, handle uintptr, name string) {
	purego.RegisterLibFunc(fptr, handle, name)
}
