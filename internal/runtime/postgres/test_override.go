package postgres

import "errors"

func InsecureTestTransportOverride(environment, raw string) (bool, error) {
	switch raw {
	case "", "false":
		return false, nil
	case "true":
		if environment != "test" {
			return false, errors.New("insecure PostgreSQL transport is restricted to the test environment")
		}
		return true, nil
	default:
		return false, errors.New("insecure PostgreSQL transport override must be true or false")
	}
}
