package config

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv          string
	JwtSecretKey    string
	JwtAlgorithm    string
	TargetAwsRegion string
	RedisAddr       string
	RedisPassword   string
	RedisURL        string
}

func LoadConfig() *Config {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "local"
	}

	if appEnv != "production" {
		if err := godotenv.Load(); err != nil {
			log.Println("⚠️ No .env file discovered; falling back to native environment variables.")
		}
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

	redisURL := os.Getenv("REDIS_URL")

	var secret string

	if appEnv == "production" {
		log.Println("☁️ Production environment detected. Fetching secrets from AWS Parameter Store...")

		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(region))
		if err != nil {
			log.Fatalf("CRITICAL: Unable to load AWS SDK config footprint: %v", err)
		}

		ssmClient := ssm.NewFromConfig(awsCfg)
		paramName := "/flagship/prod/jwt-secret"

		out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           &paramName,
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			log.Fatalf("CRITICAL: Flagship failed to load production secret from SSM Parameter Store: %v", err)
		}

		secret = *out.Parameter.Value
		log.Println("✅ Successfully loaded production JWT secret from AWS Systems Manager.")
	} else {
		secret = os.Getenv("JWT_SECRET_KEY")
		if secret == "" {
			log.Fatal("CRITICAL: JWT_SECRET_KEY environment variable is missing for local development!")
		}
	}

	return &Config{
		AppEnv:          appEnv,
		JwtSecretKey:    secret,
		JwtAlgorithm:    algorithm,
		TargetAwsRegion: region,
		RedisAddr:       redisAddr,
		RedisPassword:   os.Getenv("REDIS_PASSWORD"),
		RedisURL:        redisURL,
	}
}
