package common

type Connection struct {
	LocalPort int    `mapstructure:"local-port" json:"localPort"`
	LocalHost string `mapstructure:"local-host" json:"localHost"`
	WebPort   int    `mapstructure:"web-port" json:"webPort"`
	TaskPort  int    `mapstructure:"task-port" json:"taskPort"`
	Secret    string `mapstructure:"secret" json:"secret"`
}
