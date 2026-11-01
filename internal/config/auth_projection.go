package config

type AuthProjectionConfig struct{ URL string }

func (a AuthProjectionConfig) Enabled() bool { return false }

func (a AuthProjectionConfig) Validate(AuthWorkloadConfig) error { return nil }

func authProjectionValue(lookupEnv) AuthProjectionConfig { return AuthProjectionConfig{} }
