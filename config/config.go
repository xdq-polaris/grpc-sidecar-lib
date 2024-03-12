package config

type SidecarConfig struct {
	Port           int    `toml:"port"`
	BackendAddress string `toml:"backendAddress"`
}
