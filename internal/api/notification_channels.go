package api

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

func (h *Handler) checkReminderChannel(w http.ResponseWriter, r *http.Request, userID uuid.UUID, reminders []reminderRequest) bool {
	for _, reminder := range reminders {
		if reminder.Channel != "telegram" {
			writeError(w, http.StatusBadRequest, "only a verified and consented telegram channel can receive reminders")
			return false
		}
	}
	if len(reminders) == 0 {
		return true
	}
	if h.internalToken == "" || h.notifyInternalBaseURL == "" {
		writeError(w, http.StatusServiceUnavailable, "notification channel validation unavailable")
		return false
	}
	endpoint := strings.TrimRight(h.notifyInternalBaseURL, "/") + "/internal/users/" + url.PathEscape(userID.String()) + "/channels"
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, endpoint, nil)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "notification channel validation unavailable")
		return false
	}
	req.Header.Set("Authorization", "Bearer "+h.internalToken)
	response, err := h.internalHTTPClient.Do(req)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "notification channel validation unavailable")
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		writeError(w, http.StatusServiceUnavailable, "notification channel validation unavailable")
		return false
	}
	var result struct {
		UserID   string `json:"user_id"`
		Channels []struct {
			Channel  string `json:"channel"`
			Verified bool   `json:"verified"`
			Consent  bool   `json:"consent"`
		} `json:"channels"`
	}
	if json.NewDecoder(response.Body).Decode(&result) != nil || result.UserID != userID.String() {
		writeError(w, http.StatusServiceUnavailable, "notification channel validation unavailable")
		return false
	}
	for _, channel := range result.Channels {
		if channel.Channel == "telegram" && channel.Verified && channel.Consent {
			return true
		}
	}
	writeError(w, http.StatusConflict, "no verified and consented telegram channel is available")
	return false
}

func validInternalBearer(request *http.Request, expected string) bool {
	if expected == "" {
		return false
	}
	got := request.Header.Get("Authorization")
	want := "Bearer " + expected
	return len(got) == len(want) && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}
