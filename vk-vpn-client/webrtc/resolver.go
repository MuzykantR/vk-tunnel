package webrtc

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os/exec"
	"strings"
	"time"
)

type vkCaptchaError struct {
	captchaSid     string
	redirectURI    string
	captchaTs      string
	captchaAttempt string
}

func parseVKCaptchaError(errObj map[string]interface{}) *vkCaptchaError {
	redirectURI, _ := errObj["redirect_uri"].(string)
	if redirectURI == "" {
		return nil
	}
	captchaSid := ""
	if sid, ok := errObj["captcha_sid"].(string); ok {
		captchaSid = sid
	} else if sidNum, ok := errObj["captcha_sid"].(float64); ok {
		captchaSid = fmt.Sprintf("%.0f", sidNum)
	}
	captchaTs, _ := errObj["captcha_ts"].(string)
	captchaAttempt, _ := errObj["captcha_attempt"].(string)
	return &vkCaptchaError{
		captchaSid:     captchaSid,
		redirectURI:    redirectURI,
		captchaTs:      captchaTs,
		captchaAttempt: captchaAttempt,
	}
}

// AuthParams — результат auth/join, как RunVKAuth в whitelist-bypass.
type AuthParams struct {
	SessionKey      string   `json:"sessionKey"`
	ApplicationKey  string   `json:"applicationKey"`
	APIBaseURL      string   `json:"apiBaseURL"`
	JoinLink        string   `json:"joinLink"`
	AnonymToken     string   `json:"anonymToken"`
	AppVersion      string   `json:"appVersion"`
	ProtocolVersion string   `json:"protocolVersion"`
	StunURLs        []string `json:"-"`
	TurnURLs        []string `json:"-"`
	TurnUser        string   `json:"-"`
	TurnCred        string   `json:"-"`
}

// ResolveJoinLink performs HTTP requests to login.vk.com and api.vk.com
// to get an anonymous token and resolve the joinLink into a WebSocket URL.
// Captcha is handled autonomously via a local proxy + browser.
func ResolveJoinLink(joinLink string) (*AuthParams, error) {
	client := &http.Client{Timeout: 30 * time.Second}

	httpPost := func(targetURL string, form url.Values, extraHeaders map[string]string) (map[string]interface{}, error) {
		req, _ := http.NewRequest("POST", targetURL, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36")
		req.Header.Set("Origin", "https://vk.com")
		req.Header.Set("Referer", "https://vk.com/")
		for k, v := range extraHeaders {
			req.Header.Set(k, v)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}
		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("json: %w (body: %s)", err, string(body))
		}
		return result, nil
	}

	appID := "6287487"
	apiVersion := "5.276"
	appVersion := "1.1"

	// 1. Get anonymous token
	anonResp, err := httpPost("https://login.vk.com/?act=get_anonym_token", url.Values{
		"client_id": {appID},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("get_anonym_token: %w", err)
	}
	dataMap, _ := anonResp["data"].(map[string]interface{})
	accessToken, _ := dataMap["access_token"].(string)
	if accessToken == "" {
		return nil, fmt.Errorf("empty access_token: %v", anonResp)
	}
	auth := map[string]string{"Authorization": "Bearer " + accessToken}

	// 2. Get call settings (public key)
	settingsResp, err := httpPost("https://api.vk.com/method/calls.getSettings", url.Values{
		"v": {apiVersion},
	}, auth)
	if err != nil {
		return nil, fmt.Errorf("calls.getSettings: %w", err)
	}
	var pubKey string
	if respObj, ok := settingsResp["response"].(map[string]interface{}); ok {
		if settings, ok := respObj["settings"].(map[string]interface{}); ok {
			if pk, ok := settings["public_key"].(string); ok {
				pubKey = pk
			}
		}
	}

	// 3. Get call preview
	var okJoinLink string
	previewResp, err := httpPost("https://api.vk.com/method/calls.getCallPreview", url.Values{
		"v":            {apiVersion},
		"vk_join_link": {joinLink},
	}, auth)
	if err == nil {
		if respObj, ok := previewResp["response"].(map[string]interface{}); ok {
			if okLink, ok := respObj["ok_join_link"].(string); ok {
				okJoinLink = okLink
			}
		}
	}

	// 4. Get call token (with interactive Turnstile captcha retry loop)
	callParams := url.Values{
		"v":            {apiVersion},
		"vk_join_link": {joinLink},
		"name":         {"Joiner"},
	}

	var callToken string
	var apiBaseURL string

	for attempt := 0; attempt < 5; attempt++ {
		callResp, err := httpPost("https://api.vk.com/method/calls.getAnonymousToken", callParams, auth)
		if err != nil {
			return nil, fmt.Errorf("getAnonymousToken: %w", err)
		}

		if errObj, hasErr := callResp["error"].(map[string]interface{}); hasErr {
			errCode, _ := errObj["error_code"].(float64)
			if int(errCode) == 14 {
				captchaErr := parseVKCaptchaError(errObj)
				if captchaErr == nil {
					return nil, fmt.Errorf("captcha error missing fields: %v", errObj)
				}

				log.Println("Captcha required by VK. Launching browser for interactive verification...")

				proxyPort := StartCaptchaProxy(captchaErr.redirectURI, nil)
				if proxyPort == 0 {
					return nil, fmt.Errorf("failed to start captcha proxy")
				}

				// Open the user's default browser
				captchaURL := fmt.Sprintf("http://127.0.0.1:%d/", proxyPort)
				log.Printf("Opening captcha in browser: %s", captchaURL)
				exec.Command("cmd", "/c", "start", captchaURL).Run()

				// Block until the user solves the captcha (up to 5 minutes)
				successToken := GetCaptchaResult()
				StopCaptchaProxy()

				if successToken == "" {
					return nil, fmt.Errorf("captcha timed out or was not solved")
				}

				log.Println("Captcha solved successfully! Retrying API call...")

				captchaAttempt := captchaErr.captchaAttempt
				if captchaAttempt == "" || captchaAttempt == "0" {
					captchaAttempt = "1"
				}
				callParams = url.Values{
					"v":               {apiVersion},
					"vk_join_link":    {joinLink},
					"name":            {"Joiner"},
					"captcha_key":     {""},
					"captcha_sid":     {captchaErr.captchaSid},
					"is_sound_captcha": {"0"},
					"success_token":   {successToken},
					"captcha_ts":      {captchaErr.captchaTs},
					"captcha_attempt": {captchaAttempt},
				}
				continue
			}
			return nil, fmt.Errorf("VK API error: %v", errObj)
		}

		respMap, ok := callResp["response"].(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("unexpected response: %v", callResp)
		}
		callToken, _ = respMap["token"].(string)
		apiBaseURL, _ = respMap["api_base_url"].(string)

		if okJoinLink == "" {
			okJoinLink, _ = respMap["ok_join_link"].(string)
		}
		break
	}

	if callToken == "" {
		return nil, fmt.Errorf("failed to get call token after retries")
	}

	// 5. OK.ru anonymLogin
	baseURL := strings.TrimRight(apiBaseURL, "/")
	if !strings.HasSuffix(baseURL, "/fb.do") {
		baseURL += "/fb.do"
	}
	deviceID := fmt.Sprintf("%d", rand.Int63n(9e18))
	// session_data must NOT include auth_token — OK.ru rejects it (error 100).
	// The call token is passed separately as anonymToken in joinConversationByLink.
	sessionData, _ := json.Marshal(map[string]interface{}{
		"version":        2,
		"device_id":      deviceID,
		"client_version": appVersion,
		"client_type":    "SDK_JS",
	})
	okResp, err := httpPost(baseURL, url.Values{
		"method":          {"auth.anonymLogin"},
		"session_data":    {string(sessionData)},
		"application_key": {pubKey},
		"format":          {"json"},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("anonymLogin: %w", err)
	}
	sessionKey, _ := okResp["session_key"].(string)
	if sessionKey == "" {
		return nil, fmt.Errorf("missing session_key: %v", okResp)
	}

	// 6. Join conversation
	ms, _ := json.Marshal(map[string]bool{
		"isAudioEnabled": false, "isVideoEnabled": true, "isScreenSharingEnabled": false,
	})
	r, err := httpPost(baseURL, url.Values{
		"method":          {"vchat.joinConversationByLink"},
		"session_key":     {sessionKey},
		"application_key": {pubKey},
		"format":          {"json"},
		"joinLink":        {okJoinLink},
		"anonymToken":     {callToken},
		"isVideo":         {"true"},
		"isAudio":         {"false"},
		"mediaSettings":   {string(ms)},
	}, nil)
	if err != nil {
		return nil, fmt.Errorf("joinConversation: %w", err)
	}
	endpoint, _ := r["endpoint"].(string)
	if endpoint == "" {
		return nil, fmt.Errorf("empty WS endpoint: %v", r)
	}

	result := &AuthParams{
		SessionKey:      sessionKey,
		ApplicationKey:  pubKey,
		APIBaseURL:      baseURL,
		JoinLink:        okJoinLink,
		AnonymToken:     callToken,
		AppVersion:      appVersion,
		ProtocolVersion: "6",
	}
	if turn, ok := r["turn_server"].(map[string]interface{}); ok {
		result.TurnUser, _ = turn["username"].(string)
		result.TurnCred, _ = turn["credential"].(string)
		if urls, ok := turn["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					result.TurnURLs = append(result.TurnURLs, s)
				}
			}
		}
	}
	if stun, ok := r["stun_server"].(map[string]interface{}); ok {
		if urls, ok := stun["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					result.StunURLs = append(result.StunURLs, s)
				}
			}
		}
	}
	_ = endpoint // joiner повторно вызывает joinConversationByLink в joinCall()
	return result, nil
}
