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

	return &Config{
		AppEnv:          os.Getenv("APP_ENV"),
		JwtSecretKey:    secret,
		JwtAlgorithm:    algorithm,
		TargetAwsRegion: region,
	}
}
