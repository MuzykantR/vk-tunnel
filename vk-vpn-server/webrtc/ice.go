package webrtc

import "encoding/json"

type ICEConfig struct {
	StunURLs []string
	TurnURLs []string
	TurnUser string
	TurnCred string
}

func ParseICEFromJoin(body []byte) ICEConfig {
	var cfg ICEConfig
	var r map[string]interface{}
	if json.Unmarshal(body, &r) != nil {
		return cfg
	}
	if turn, ok := r["turn_server"].(map[string]interface{}); ok {
		cfg.TurnUser, _ = turn["username"].(string)
		cfg.TurnCred, _ = turn["credential"].(string)
		if urls, ok := turn["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					cfg.TurnURLs = append(cfg.TurnURLs, s)
				}
			}
		}
	}
	if stun, ok := r["stun_server"].(map[string]interface{}); ok {
		if urls, ok := stun["urls"].([]interface{}); ok {
			for _, u := range urls {
				if s, ok := u.(string); ok {
					cfg.StunURLs = append(cfg.StunURLs, s)
				}
			}
		}
	}
	return cfg
}
