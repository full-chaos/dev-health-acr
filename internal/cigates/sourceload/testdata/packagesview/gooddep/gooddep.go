package gooddep

import "unsafe"

type Box struct{ N int }

func Size() uintptr { return unsafe.Sizeof(Box{}) }

func Value() int { return 7 }
