package good

import "github.com/full-chaos/dev-health-acr/internal/cigates/sourceload/testdata/packagesview/gooddep"

func Read() int {
	b := gooddep.Box{N: gooddep.Value()}
	return b.N
}
