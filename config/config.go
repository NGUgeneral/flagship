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
	AppHost         string
	JwtSecretKey    string
	JwtAlgorithm    string
	TargetAwsRegion string
	RedisAddr       string
	RedisPassword   string
	RedisURL        string
	RedisHashKey    string
	RateLimiterURL  string
}

func LoadConfig() *Config {
	appEnv := os.Getenv("APP_ENV")
	if appEnv == "" {
		appEnv = "local"
	}

	appHost := os.Getenv("APP_HOST")
	if appHost == "" {
		appHost = "localhost:8080"
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

	redisPassword := os.Getenv("REDIS_PASSWORD")

	redisURL := os.Getenv("REDIS_URL")

	redisHashKey := os.Getenv("REDIS_HASH_KEY")
	if redisHashKey == "" {
		redisHashKey = "headsntails:v1:flags"
	}

	rateLimiterURL := os.Getenv("RATE_LIMITER_URL")
	if rateLimiterURL == "" {
		log.Fatal("CRITICAL: RATE_LIMITER_URL environment variable is required but not set!")
	}

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
		paramName := "/headsntails-core/prod/jwt-secret"

		out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
			Name:           &paramName,
			WithDecryption: aws.Bool(true),
		})
		if err != nil {
			log.Fatalf("CRITICAL: headsntails failed to load production secret from SSM Parameter Store: %v", err)
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
		AppHost:         appHost,
		JwtSecretKey:    secret,
		JwtAlgorithm:    algorithm,
		TargetAwsRegion: region,
		RedisAddr:       redisAddr,
		RedisPassword:   redisPassword,
		RedisURL:        redisURL,
		RedisHashKey:    redisHashKey,
		RateLimiterURL:  rateLimiterURL,
	}
}
