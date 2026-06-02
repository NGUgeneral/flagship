package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

type contextKey string

const ContextAudienceKey contextKey = "token_audience"

type CustomClaims struct {
	Type string `json:"type"`
	jwt.RegisteredClaims
}

func AuthMiddleware(jwtSecret []byte, expectedAlg string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			authHeader := r.Header.Get("Authorization")
			if authHeader == "" {
				http.Error(w, "Unauthorized: Missing Authorization header", http.StatusUnauthorized)
				return
			}

			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
				http.Error(w, "Unauthorized: Malformed Authorization header scheme", http.StatusUnauthorized)
				return
			}

			tokenString := parts[1]
			claims := &CustomClaims{}

			token, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (interface{}, error) {
				if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
					return nil, fmt.Errorf("unexpected signing method family: %v", t.Header["alg"])
				}

				if t.Method.Alg() != expectedAlg {
					return nil, fmt.Errorf("unexpected dynamic signing method: expected %s, got %s", expectedAlg, t.Method.Alg())
				}

				return jwtSecret, nil
			})

			if err != nil || !token.Valid {
				http.Error(w, "Unauthorized: Invalid or expired access token", http.StatusUnauthorized)
				return
			}

			if claims.Type != "access" {
				http.Error(w, "Unauthorized: Invalid token context scope", http.StatusUnauthorized)
				return
			}

			audience, err := claims.GetAudience()
			if err != nil || len(audience) == 0 {
				http.Error(w, "Unauthorized: Missing identity profile claims", http.StatusUnauthorized)
				return
			}

			ctx := context.WithValue(r.Context(), ContextAudienceKey, audience[0])
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}
