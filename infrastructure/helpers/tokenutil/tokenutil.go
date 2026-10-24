package tokenutil

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"github.com/labbs/ogit/infrastructure/config"
)

func CreateAccessToken(user_id, sessionId string, config config.Config) (accessToken string, err error) {
	exp := time.Now().Add(time.Minute * time.Duration(config.Session.ExpirationMinutes)).Unix()
	claims := &JwtCustomClaims{
		SessionID: sessionId,
		UserID:    user_id,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    config.Session.Issuer,
			ExpiresAt: &jwt.NumericDate{Time: time.Unix(exp, 0)},
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t, err := token.SignedString([]byte(config.Session.SecretKey))
	if err != nil {
		return "", err
	}
	return t, nil
}

func GetSessionIdFromToken(tokenString string, config config.Config) (string, error) {
	// Only accept the algorithm CreateAccessToken signs with.
	token, err := jwt.ParseWithClaims(tokenString, &JwtCustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		return []byte(config.Session.SecretKey), nil
	}, jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil {
		return "", err
	}
	claims, ok := token.Claims.(*JwtCustomClaims)
	if !ok || !token.Valid || claims.SessionID == "" {
		return "", errors.New("invalid token claims")
	}
	return claims.SessionID, nil
}
