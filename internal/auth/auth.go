package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/alexedwards/argon2id"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
)

func HashPassword(password string) (string, error) {
	hash, err := argon2id.CreateHash(password, argon2id.DefaultParams)
	if err != nil {
		return "", err
	}
	return hash, nil
}

func CheckPasswordHash(password, hash string) (bool, error) {
	boolValue, err := argon2id.ComparePasswordAndHash(password, hash)
	if err != nil {
		return false, err
	}
	return boolValue, nil
}

func MakeJWT(userID uuid.UUID, tokenSecret string, expiresIn time.Duration) (string, error) {
	token := jwt.NewWithClaims(
		jwt.SigningMethodHS256,
		jwt.RegisteredClaims{
			Issuer:    "chirpy-access",
			IssuedAt:  jwt.NewNumericDate(time.Now().UTC()),
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(expiresIn)),
			Subject:   userID.String(),
		},
	)
	return token.SignedString([]byte(tokenSecret))
}

func ValidateJWT(tokenString, tokenSecret string) (uuid.UUID, error) {
	token, err := jwt.ParseWithClaims(tokenString, &jwt.RegisteredClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(tokenSecret), nil
	},
	)
	if err != nil {
		return uuid.Nil, err
	}
	subject, err := token.Claims.GetSubject()
	if err != nil {
		return uuid.Nil, err
	}
	uuidString, err := uuid.Parse(subject)
	if err != nil {
		return uuid.Nil, err
	}
	return uuidString, nil
}

func GetBearerToken(headers http.Header) (string, error) {
	authHeader := headers.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("No auth token")
	}
	parts := strings.Split(authHeader, " ")
	if len(parts) > 1 && parts[0] == "Bearer" {
		return parts[1], nil
	} else {
		return "", errors.New("No auth token")
	}
}

func MakeRefreshToken() string {
	entropy := make([]byte, 32)
	_, err := rand.Read(entropy)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(entropy)
}

func GetAPIKey(headers http.Header) (string, error) {
	authHeader := headers.Get("Authorization")
	if authHeader == "" {
		return "", errors.New("No API key")
	}
	parts := strings.Split(authHeader, " ")
	if len(parts) > 1 && parts[0] == "ApiKey" {
		return parts[1], nil
	} else {
		return "", errors.New("No API key")
	}
}
