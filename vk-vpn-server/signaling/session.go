package signaling

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"
)

// CallSession holds a VK call that can be rejoined without calls.start (whitelist-bypass model).
type CallSession struct {
	VKJoinLink string
	OKJoinLink string
	StartedAt  time.Time
}

// RejoinConversation re-authenticates and joins the same OK join token, returning a fresh WS URL.
func (s *VKSignaling) RejoinConversation(ctx context.Context, okJoinLink string) (wsURL string, joinBody []byte, err error) {
	appID := "6287487"
	apiVersion := "5.276"

	r, err := s.httpPost("https://login.vk.com/?act=web_token", url.Values{"version": {"1"}, "app_id": {appID}}, nil)
	if err != nil {
		return "", nil, fmt.Errorf("web_token: %w", err)
	}
	var tok struct {
		Data struct {
			AccessToken string `json:"access_token"`
		} `json:"data"`
	}
	json.Unmarshal(r, &tok)
	if tok.Data.AccessToken == "" {
		return "", nil, fmt.Errorf("empty VK token")
	}
	auth := map[string]string{"Authorization": "Bearer " + tok.Data.AccessToken}

	r, err = s.httpPost("https://api.vk.com/method/calls.getSettings", url.Values{"v": {apiVersion}}, auth)
	if err != nil {
		return "", nil, err
	}
	var settings struct {
		Response struct {
			Settings struct {
				PublicKey string `json:"public_key"`
			} `json:"settings"`
		} `json:"response"`
	}
	json.Unmarshal(r, &settings)
	appKey := settings.Response.Settings.PublicKey

	r, err = s.httpPost("https://api.vk.com/method/messages.getCallToken", url.Values{"v": {apiVersion}, "env": {"production"}}, auth)
	if err != nil {
		return "", nil, err
	}
	var callToken struct {
		Response struct {
			Token      string `json:"token"`
			APIBaseURL string `json:"api_base_url"`
		} `json:"response"`
	}
	json.Unmarshal(r, &callToken)
	apiBaseURL := strings.TrimRight(callToken.Response.APIBaseURL, "/")
	if !strings.HasSuffix(apiBaseURL, "/fb.do") {
		apiBaseURL += "/fb.do"
	}

	sd, _ := json.Marshal(map[string]interface{}{
		"device_id": "headless-vpn-1", "client_version": "1.1",
		"client_type": "SDK_JS", "auth_token": callToken.Response.Token, "version": 3,
	})
	r, err = s.httpPost(apiBaseURL, url.Values{
		"method": {"auth.anonymLogin"}, "application_key": {appKey},
		"format": {"json"}, "session_data": {string(sd)},
	}, nil)
	if err != nil {
		return "", nil, err
	}
	var okAuth struct {
		SessionKey string `json:"session_key"`
	}
	json.Unmarshal(r, &okAuth)

	ms, _ := json.Marshal(map[string]bool{
		"isAudioEnabled": false, "isVideoEnabled": true, "isScreenSharingEnabled": false,
	})
	r, err = s.httpPost(apiBaseURL, url.Values{
		"method":          {"vchat.joinConversationByLink"},
		"session_key":     {okAuth.SessionKey},
		"application_key": {appKey},
		"format":          {"json"},
		"joinLink":        {okJoinLink},
		"isVideo":         {"true"},
		"isAudio":         {"false"},
		"mediaSettings":   {string(ms)},
	}, nil)
	if err != nil {
		return "", nil, err
	}
	var joinResp struct {
		Endpoint string `json:"endpoint"`
	}
	json.Unmarshal(r, &joinResp)
	if joinResp.Endpoint == "" {
		return "", nil, fmt.Errorf("empty WS endpoint on rejoin")
	}
	ws := joinResp.Endpoint + "&platform=WEB&appVersion=1.1&version=6&device=browser&capabilities=0&clientType=VK&tgt=join"
	log.Printf("[signaling] Rejoined existing call (OK token), new WS endpoint")
	return ws, r, nil
}
