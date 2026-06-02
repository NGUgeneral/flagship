package config

import (
	"log"
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv          string
	JwtSecretKey    string
	JwtAlgorithm    string
	TargetAwsRegion string
	RedisAddr       string
	RedisPassword   string
}

func LoadConfig() *Config {
	// Only attempt to load .env if we are running locally
	if os.Getenv("APP_ENV") != "production" {
		if err := godotenv.Load(); err != nil {
			log.Println("⚠️ No .env file discovered; falling back to native environment variables.")
		}
	}

	secret := os.Getenv("JWT_SECRET_KEY")
	if secret == "" {
		log.Fatal("CRITICAL: JWT_SECRET_KEY environment variable is missing!")
	}

	algorithm := os.Getenv("JWT_ALGORITHM")
	if algorithm == "" {
		algorithm = "HS256"
	}

	region := os.Getenv("TARGET_AWS_REGION")
	if region == "" {
		region = "eu-central-1"
	}

	redisAddr := os.Getenv("REDIS_ADDR")
	if redisAddr == "" {
		redisAddr = "localhost:6379"
	}

	return &Config{
		AppEnv:          os.Getenv("APP_ENV"),
		JwtSecretKey:    secret,
		JwtAlgorithm:    algorithm,
		TargetAwsRegion: region,
		RedisAddr:       redisAddr,
		RedisPassword:   os.Getenv("REDIS_PASSWORD"),
	}
}
