// Package logx — единый компактный формат логов vk-vpn (без дублирования даты с journald).
//
// Формат строки: 15:04:05.000 [tag] message
// journald/systemd добавляет свою метку времени слева — Go не дублирует 2026/05/19.
package logx

import (
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"
	"time"
)

type Level int32

const (
	LevelError Level = iota
	LevelWarn
	LevelInfo
	LevelDebug
)

var currentLevel atomic.Int32 // LevelInfo

func Init() {
	log.SetFlags(0)
	log.SetOutput(os.Stderr)
	lvl := strings.ToLower(strings.TrimSpace(os.Getenv("VK_VPN_LOG_LEVEL")))
	switch lvl {
	case "debug", "trace":
		currentLevel.Store(int32(LevelDebug))
	case "warn", "warning":
		currentLevel.Store(int32(LevelWarn))
	case "error":
		currentLevel.Store(int32(LevelError))
	default:
		currentLevel.Store(int32(LevelInfo))
	}
}

func enabled(l Level) bool {
	return Level(currentLevel.Load()) >= l
}

// L writes one line: HH:MM:SS.mmm [tag] message
func L(tag, format string, args ...interface{}) {
	if !enabled(LevelInfo) {
		return
	}
	write(tag, format, args...)
}

func Debug(tag, format string, args ...interface{}) {
	if !enabled(LevelDebug) {
		return
	}
	write(tag, format, args...)
}

func Warn(tag, format string, args ...interface{}) {
	if !enabled(LevelWarn) {
		return
	}
	write(tag, format, args...)
}

func Error(tag, format string, args ...interface{}) {
	write(tag, format, args...)
}

func write(tag, format string, args ...interface{}) {
	msg := fmt.Sprintf(format, args...)
	// std log adds nothing when flags=0; timestamp only here
	log.Printf("%s [%s] %s", time.Now().Format("15:04:05.000"), tag, msg)
}

// Printf adapts to tunnel.RelayBridge logFn signature.
func Printf(tag, format string, args ...interface{}) {
	L(tag, format, args...)
}

func TagPrintf(tag string) func(string, ...interface{}) {
	return func(format string, args ...interface{}) {
		L(tag, format, args...)
	}
}
