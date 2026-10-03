package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port              string
	MySQLHost         string
	MySQLPort         string
	MySQLUser         string
	MySQLPassword     string
	MySQLDatabase     string
	RedisHost         string
	RedisPort         string
	RedisPassword     string
	VictoriaLogsURL   string
	DefaultUSDtoCNY   float64
}

func LoadConfig() *Config {
	port := os.Getenv("PORT")
	if port == "" {
		port = os.Getenv("LITELLM_PORT")
	}
	if port == "" {
		port = "4000"
	}

	fxRate := 7.2300
	if rawFx := os.Getenv("DEFAULT_USD_TO_CNY_RATE"); rawFx != "" {
		if f, err := strconv.ParseFloat(rawFx, 64); err == nil && f > 0 {
			fxRate = f
		}
	}

	vlogsURL := os.Getenv("VICTORIALOGS_URL")
	if vlogsURL == "" {
		vlogsURL = "http://10.0.1.227:9428"
	}

	return &Config{
		Port:            port,
		MySQLHost:       getEnv("MYSQL_HOST", "161.118.240.218"),
		MySQLPort:       getEnv("MYSQL_PORT", "3306"),
		MySQLUser:       getEnv("MYSQL_USER", "litellm_user"),
		MySQLPassword:   getEnv("MYSQL_PASSWORD", ""),
		MySQLDatabase:   getEnv("MYSQL_DB", "litellm_db"),
		RedisHost:       getEnv("REDIS_HOST", "100.105.130.0"),
		RedisPort:       getEnv("REDIS_PORT", "6379"),
		RedisPassword:   getEnv("REDIS_PASSWORD", "hsbc1234"),
		VictoriaLogsURL: vlogsURL,
		DefaultUSDtoCNY: fxRate,
	}
}

func getEnv(key, defaultVal string) string {
	val := os.Getenv(key)
	if val == "" {
		return defaultVal
	}
	return val
}
