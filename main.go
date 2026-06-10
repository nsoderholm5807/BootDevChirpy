package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/joho/godotenv"
	_ "github.com/lib/pq"
	"github.com/nsoderholm5807/chirpy/internal/auth"
	"github.com/nsoderholm5807/chirpy/internal/database"
)

type apiConfig struct {
	fileserverHits atomic.Int32
	jwt            string
	db             *database.Queries
	platform       string
	apiKey         string
}

type User struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Email     string    `json:"email"`
	JWToken   string    `json:"token"`
	RToken    string    `json:"refresh_token"`
	ChirpyRed bool      `json:"is_chirpy_red"`
}

type Chirp struct {
	ID        uuid.UUID `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	Body      string    `json:"body"`
	User_id   uuid.UUID `json:"user_id"`
}

type Refresh_Token struct {
	Token     string    `json:"token"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	ID        uuid.UUID `json:"id"`
	RToken    string    `json:"refresh_token"`
	REVToken  string    `json:"revoke_token"`
}

func (cfg *apiConfig) middlewareMetricsInc(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg.fileserverHits.Add(1)
		next.ServeHTTP(w, r)
	})
}

func (cfg *apiConfig) handlerMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(fmt.Sprintf("<html><body><h1>Welcome, Chirpy Admin</h1><p>Chirpy has been visited %d times!</p></body></html>", cfg.fileserverHits.Load())))
}

func (cfg *apiConfig) handlerReset(w http.ResponseWriter, r *http.Request) {
	if cfg.platform != "dev" {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	err := cfg.db.DeleteUsers(r.Context())
	if err != nil {
		respondWithError(w, http.StatusInternalServerError, "Error deleting user")
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	cfg.fileserverHits.Store(0)
	w.Write([]byte("OK"))
}

func handlerHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

func respondWithError(w http.ResponseWriter, code int, msg string) {
	type errorResponse struct {
		Message string `json:"error"`
	}
	payload := errorResponse{
		Message: msg,
	}
	respondWithJSON(w, code, payload)
}

func respondWithJSON(w http.ResponseWriter, code int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	d, err := json.Marshal(payload)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	w.WriteHeader(code)
	w.Write(d)
}

func (cfg *apiConfig) handlerUserCreation(w http.ResponseWriter, r *http.Request) {
	decoder := json.NewDecoder(r.Body)
	type requestBody struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	params := requestBody{}
	err := decoder.Decode(&params)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	params.Password, err = auth.HashPassword(params.Password)
	if err != nil {
		w.WriteHeader(500)
		return
	}
	createdUser, err := cfg.db.CreateUser(r.Context(), database.CreateUserParams{
		Email:          params.Email,
		HashedPassword: params.Password,
	})
	if err != nil {
		w.WriteHeader(500)
		return
	}
	responseUser := User{
		Email:     createdUser.Email,
		CreatedAt: createdUser.CreatedAt,
		UpdatedAt: createdUser.UpdatedAt,
		ID:        createdUser.ID,
		ChirpyRed: createdUser.IsChirpyRed,
	}
	respondWithJSON(w, http.StatusCreated, responseUser)
}

func (cfg *apiConfig) handlerTweet(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Tweet string `json:"body"`
	}
	jwtoken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, 401, "Something went wrong")
		return
	}
	userID, err := auth.ValidateJWT(jwtoken, cfg.jwt)
	if err != nil {
		respondWithError(w, 401, "Something went wrong")
		return
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		respondWithError(w, 500, "Something went wrong")
		return
	}

	if len(params.Tweet) > 140 {
		respondWithError(w, 400, "Chirp is too long")
		return
	}
	messageBody := strings.Split(params.Tweet, " ")
	for i, word := range messageBody {
		lword := strings.ToLower(word)
		if lword == "kerfuffle" || lword == "sharbert" || lword == "fornax" {
			messageBody[i] = "****"
		}
	}

	cleaned_body := strings.Join(messageBody, " ")

	createdChirp, err := cfg.db.CreateChirp(r.Context(), database.CreateChirpParams{
		Body:   cleaned_body,
		UserID: userID,
	})
	if err != nil {
		w.WriteHeader(500)
		return
	}
	responseChirp := Chirp{
		Body:      createdChirp.Body,
		CreatedAt: createdChirp.CreatedAt,
		UpdatedAt: createdChirp.UpdatedAt,
		ID:        createdChirp.ID,
		User_id:   userID,
	}
	respondWithJSON(w, http.StatusCreated, responseChirp)
}

func (cfg *apiConfig) handlerGetChirps(w http.ResponseWriter, r *http.Request) {
	authorParse := r.URL.Query().Get("author_id")
	sortValue := r.URL.Query().Get("sort")
	responseChirps := []database.Chirp{}
	var err error
	if authorParse != "" {
		author, parseErr := uuid.Parse(authorParse)
		if parseErr != nil {
			w.WriteHeader(500)
			return
		}
		responseChirps, err = cfg.db.GetChirpsForAuthor(r.Context(), author)
		if err != nil {
			w.WriteHeader(500)
			return
		}
	} else {
		responseChirps, err = cfg.db.GetChirps(r.Context())
		if err != nil {
			w.WriteHeader(500)
			return
		}
	}

	responseSlice := []Chirp{}
	for _, chirp := range responseChirps {
		slice := Chirp{
			Body:      chirp.Body,
			CreatedAt: chirp.CreatedAt,
			UpdatedAt: chirp.UpdatedAt,
			ID:        chirp.ID,
			User_id:   chirp.UserID,
		}
		responseSlice = append(responseSlice, slice)
	}

	sort.Slice(responseSlice, func(i, j int) bool {
		if sortValue == "desc" {
			return responseSlice[i].CreatedAt.After(responseSlice[j].CreatedAt)
		}
		return responseSlice[i].CreatedAt.Before(responseSlice[j].CreatedAt)
	})

	respondWithJSON(w, http.StatusOK, responseSlice)
}

func (cfg *apiConfig) handlerGetChirp(w http.ResponseWriter, r *http.Request) {
	chirpParse := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpParse)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Invalid chirp ID")
		return
	}
	responseChirp, err := cfg.db.GetChirp(r.Context(), chirpID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	response := Chirp{
		Body:      responseChirp.Body,
		CreatedAt: responseChirp.CreatedAt,
		UpdatedAt: responseChirp.UpdatedAt,
		ID:        responseChirp.ID,
		User_id:   responseChirp.UserID,
	}
	respondWithJSON(w, http.StatusOK, response)
}

func (cfg *apiConfig) handlerLogin(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Email            string `json:"email"`
		Password         string `json:"password"`
		ExpiresInSeconds *int   `json:"expires_in_seconds"`
	}

	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err := decoder.Decode(&params)
	if err != nil {
		respondWithError(w, 500, "Something went wrong")
		return
	}

	responseLogin, err := cfg.db.UserLogin(r.Context(), params.Email)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Incorrect email or password")
		return
	}
	hashCheck, err := auth.CheckPasswordHash(params.Password, responseLogin.HashedPassword)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Incorrect email or password")
		return
	}

	if hashCheck {
		rToken := auth.MakeRefreshToken()
		_, err = cfg.db.CreateRefreshToken(r.Context(), database.CreateRefreshTokenParams{
			Token:     rToken,
			UserID:    responseLogin.ID,
			ExpiresAt: time.Now().Add(60 * 24 * time.Hour),
		})
		if err != nil {
			respondWithError(w, http.StatusInternalServerError, "Error storing refresh token")
			return
		}

		token, err := auth.MakeJWT(responseLogin.ID, cfg.jwt, time.Hour)
		if err != nil {
			respondWithError(w, http.StatusUnauthorized, "Error creating Token")
			return
		}
		responseUser := User{
			Email:     responseLogin.Email,
			CreatedAt: responseLogin.CreatedAt,
			UpdatedAt: responseLogin.UpdatedAt,
			ID:        responseLogin.ID,
			JWToken:   token,
			RToken:    rToken,
			ChirpyRed: responseLogin.IsChirpyRed,
		}
		respondWithJSON(w, http.StatusOK, responseUser)
	} else {
		respondWithError(w, http.StatusUnauthorized, "Incorrect email or password")
		return
	}
}

func (cfg *apiConfig) handlerRefreshToken(w http.ResponseWriter, r *http.Request) {
	type token struct {
		Token string `json:"token"`
	}
	authToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	rToken, err := cfg.db.GetUserFromRefreshToken(r.Context(), authToken)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	JWTToken, err := auth.MakeJWT(rToken.ID, cfg.jwt, time.Hour)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid JWT Token")
		return
	}
	responseToken := token{
		Token: JWTToken,
	}
	respondWithJSON(w, http.StatusOK, responseToken)
}

func (cfg *apiConfig) handlerRevokeToken(w http.ResponseWriter, r *http.Request) {
	authToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	err = cfg.db.RevokeRefreshToken(r.Context(), authToken)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (cfg *apiConfig) handlerUpdateUser(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	authToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	userID, err := auth.ValidateJWT(authToken, cfg.jwt)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Something went wrong")
		return
	}
	hashPassword, err := auth.HashPassword(params.Password)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Something went wrong")
		return
	}
	user, err := cfg.db.UpdateUser(r.Context(),
		database.UpdateUserParams{
			ID:             userID,
			Email:          params.Email,
			HashedPassword: hashPassword,
		})
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Something went wrong")
		return
	}
	responseUser := User{
		ID:        user.ID,
		CreatedAt: user.CreatedAt,
		UpdatedAt: user.UpdatedAt,
		Email:     user.Email,
		ChirpyRed: user.IsChirpyRed,
	}
	respondWithJSON(w, http.StatusOK, responseUser)
}

func (cfg *apiConfig) handlerDeleteChirp(w http.ResponseWriter, r *http.Request) {
	authToken, err := auth.GetBearerToken(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	userID, err := auth.ValidateJWT(authToken, cfg.jwt)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Bearer Token")
		return
	}
	chirpParse := r.PathValue("chirpID")
	chirpID, err := uuid.Parse(chirpParse)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	responseChirp, err := cfg.db.GetChirp(r.Context(), chirpID)
	if err != nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if userID != responseChirp.UserID {
		respondWithError(w, http.StatusForbidden, "Not Authorized")
		return
	}
	err = cfg.db.DeleteChirp(r.Context(), chirpID)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "Chirp not Found")
		return
	}
	w.WriteHeader(http.StatusNoContent)

}

func (cfg *apiConfig) handlerUpgradeUser(w http.ResponseWriter, r *http.Request) {
	type parameters struct {
		Event string `json:"event"`
		Data  struct {
			User_id uuid.UUID `json:"user_id"`
		}
	}

	authKey, err := auth.GetAPIKey(r.Header)
	if err != nil {
		respondWithError(w, http.StatusUnauthorized, "Invalid Api Key")
		return
	}
	if cfg.apiKey != authKey {
		respondWithError(w, http.StatusUnauthorized, "Invalid Api Key")
		return
	}
	decoder := json.NewDecoder(r.Body)
	params := parameters{}
	err = decoder.Decode(&params)
	if err != nil {
		respondWithError(w, http.StatusBadRequest, "Something went wrong")
		return
	}
	if params.Event != "user.upgraded" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	_, err = cfg.db.UpgradeUser(r.Context(), params.Data.User_id)
	if err != nil {
		respondWithError(w, http.StatusNotFound, "User not Found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	godotenv.Load()
	dbURL := os.Getenv("DB_URL")
	platform := os.Getenv("PLATFORM")
	jwt := os.Getenv("JWT_SECRET")
	db, err := sql.Open("postgres", dbURL)
	apiKey := os.Getenv("POLKA_KEY")
	if err != nil {
		log.Fatal(err)
	}
	dbQueries := database.New(db)
	myMux := http.NewServeMux()
	apiCfg := apiConfig{
		jwt:      jwt,
		db:       dbQueries,
		platform: platform,
		apiKey:   apiKey,
	}
	handler := http.FileServer(http.Dir("."))

	myMux.Handle("/app/", apiCfg.middlewareMetricsInc(http.StripPrefix("/app", handler)))
	myMux.HandleFunc("GET /api/healthz", handlerHealth)
	myMux.HandleFunc("GET /admin/metrics", apiCfg.handlerMetrics)
	myMux.HandleFunc("POST /admin/reset", apiCfg.handlerReset)
	myMux.HandleFunc("POST /api/users", apiCfg.handlerUserCreation)
	myMux.HandleFunc("POST /api/chirps", apiCfg.handlerTweet)
	myMux.HandleFunc("GET /api/chirps", apiCfg.handlerGetChirps)
	myMux.HandleFunc("GET /api/chirps/{chirpID}", apiCfg.handlerGetChirp)
	myMux.HandleFunc("POST /api/login", apiCfg.handlerLogin)
	myMux.HandleFunc("POST /api/refresh", apiCfg.handlerRefreshToken)
	myMux.HandleFunc("POST /api/revoke", apiCfg.handlerRevokeToken)
	myMux.HandleFunc("PUT /api/users", apiCfg.handlerUpdateUser)
	myMux.HandleFunc("DELETE /api/chirps/{chirpID}", apiCfg.handlerDeleteChirp)
	myMux.HandleFunc("POST /api/polka/webhooks", apiCfg.handlerUpgradeUser)

	myServer := &http.Server{
		Addr:    ":8080",
		Handler: myMux,
	}

	myServer.ListenAndServe()
}
