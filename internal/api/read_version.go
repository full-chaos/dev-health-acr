package api

import "github.com/full-chaos/dev-health-acr/internal/version"

func clientVersionCompatible(value, minimum string) bool {
	return version.AtLeast(value, minimum)
}
