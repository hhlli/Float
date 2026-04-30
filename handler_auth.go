package main

import (
	"encoding/json"
	"net/http"
)

// 管理员登录
func apiLoginHandler(w http.ResponseWriter, r *http.Request) {
	var creds struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	json.NewDecoder(r.Body).Decode(&creds)

	var adminUser, adminPass, serverToken string
	db.QueryRow("SELECT value FROM settings WHERE key = 'admin_username'").Scan(&adminUser)
	db.QueryRow("SELECT value FROM settings WHERE key = 'admin_password'").Scan(&adminPass)
	db.QueryRow("SELECT value FROM settings WHERE key = 'server_token'").Scan(&serverToken)

	if creds.Username == adminUser && creds.Password == adminPass {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"status": "success",
			"token":  serverToken,
		})
	} else {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}
}