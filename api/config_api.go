package main

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"
)

func (h *WebhookHandler) HandleConfigAPI(w http.ResponseWriter, r *http.Request) {
	if !isAuthorizedAdminRequest(r) {
		respondJSONError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	if h.store == nil {
		respondJSONError(w, http.StatusServiceUnavailable, "store is not initialized")
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api")
	path = strings.TrimSpace(path)
	if path == "" {
		respondJSON(w, http.StatusOK, map[string]interface{}{"status": "ok"})
		return
	}

	switch {
	case path == "/plan-tiers":
		h.handlePlanTiers(w, r)
	case path == "/accounts":
		h.handleAccounts(w, r)
	case strings.HasPrefix(path, "/accounts/"):
		h.handleAccountSubresource(w, r, path)
	case path == "/groups":
		h.handleGroups(w, r)
	case path == "/users":
		h.handleUsers(w, r)
	case path == "/group-memberships":
		h.handleGroupMemberships(w, r)
	default:
		respondJSONError(w, http.StatusNotFound, "unknown api route")
	}
}

func (h *WebhookHandler) handlePlanTiers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := h.store.ListPlanTiers(r.Context())
		if err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var in PlanTier
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		created, err := h.store.UpsertPlanTier(r.Context(), in)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, created)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *WebhookHandler) handleAccounts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		items, err := h.store.ListAccounts(r.Context())
		if err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var in Account
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		created, err := h.store.UpsertAccount(r.Context(), in)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, map[string]interface{}{
			"data":                 created,
			"terms_and_conditions": buildKokoTermsAndConditions(),
		})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *WebhookHandler) handleAccountSubresource(w http.ResponseWriter, r *http.Request, path string) {
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) < 3 || parts[0] != "accounts" {
		respondJSONError(w, http.StatusNotFound, "unknown account route")
		return
	}

	accountID := parts[1]
	subresource := parts[2]

	switch subresource {
	case "plan":
		if r.Method != http.MethodPatch {
			methodNotAllowed(w, http.MethodPatch)
			return
		}
		var req struct {
			RequestedByUserID   *int64 `json:"requested_by_user_id"`
			RequestedByUserName string `json:"requested_by_user_name"`
			RequestedPlanTierID int64  `json:"requested_plan_tier_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}

		_, err := h.store.RecordPlanChangeRequest(r.Context(), PlanChangeRequest{
			AccountID:           accountID,
			RequestedByUserID:   req.RequestedByUserID,
			RequestedByUserName: req.RequestedByUserName,
			RequestedPlanTierID: req.RequestedPlanTierID,
		})
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}

		updated, err := h.store.UpdateAccountPlan(r.Context(), accountID, req.RequestedPlanTierID)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, updated)
	case "plan-change-requests":
		if r.Method != http.MethodPost {
			methodNotAllowed(w, http.MethodPost)
			return
		}
		var req PlanChangeRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		req.AccountID = accountID
		created, err := h.store.RecordPlanChangeRequest(r.Context(), req)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, created)
	default:
		respondJSONError(w, http.StatusNotFound, "unknown account subresource")
	}
}

func (h *WebhookHandler) handleGroups(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
		items, err := h.store.ListGroups(r.Context(), accountID)
		if err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var in Group
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		created, err := h.store.UpsertGroup(r.Context(), in)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, created)
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *WebhookHandler) handleUsers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		accountID := strings.TrimSpace(r.URL.Query().Get("account_id"))
		items, err := h.store.ListUsers(r.Context(), accountID)
		if err != nil {
			respondJSONError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, http.StatusOK, items)
	case http.MethodPost:
		var in User
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			respondJSONError(w, http.StatusBadRequest, "invalid json body")
			return
		}
		created, err := h.store.UpsertUser(r.Context(), in)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, err.Error())
			return
		}
		respondJSON(w, http.StatusCreated, map[string]interface{}{
			"data":                 created,
			"terms_and_conditions": buildKokoTermsAndConditions(),
		})
	default:
		methodNotAllowed(w, http.MethodGet, http.MethodPost)
	}
}

func (h *WebhookHandler) handleGroupMemberships(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowed(w, http.MethodPost)
		return
	}

	var req struct {
		GroupID int64  `json:"group_id"`
		UserID  int64  `json:"user_id"`
		AddedAt string `json:"added_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondJSONError(w, http.StatusBadRequest, "invalid json body")
		return
	}

	addedAt := time.Time{}
	if strings.TrimSpace(req.AddedAt) != "" {
		parsed, err := time.Parse(time.RFC3339, req.AddedAt)
		if err != nil {
			respondJSONError(w, http.StatusBadRequest, "added_at must be RFC3339")
			return
		}
		addedAt = parsed
	}

	membership, err := h.store.AddGroupMembership(r.Context(), req.GroupID, req.UserID, addedAt)
	if err != nil {
		respondJSONError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, http.StatusCreated, membership)
}

func respondJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func respondJSONError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, status, map[string]string{"error": message})
}

func methodNotAllowed(w http.ResponseWriter, methods ...string) {
	w.Header().Set("Allow", strings.Join(methods, ", "))
	respondJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
}

func isAuthorizedAdminRequest(r *http.Request) bool {
	expected := strings.TrimSpace(os.Getenv("KOKO_ADMIN_API_TOKEN"))
	if expected == "" {
		return true
	}

	header := strings.TrimSpace(r.Header.Get("Authorization"))
	if strings.HasPrefix(strings.ToLower(header), "bearer ") {
		provided := strings.TrimSpace(header[len("Bearer "):])
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1 {
			return true
		}
	}

	provided := strings.TrimSpace(r.Header.Get("X-API-Key"))
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
