# vk-vpn vs whitelist-bypass — пробелы (май 2026)

> **Прод 20.05.2026:** VP8 video mode ~**18/17 Mbps**, ICE стабилен — паритет throughput с WLB на одном звонке достигнут. См. [BENCHMARKS.md](BENCHMARKS.md).

Референс: `whitelist-bypass/relay/`, `headless/vk/`, `joiner-desktop-app/`.

## Уже перенесено

| Область | WLB | vk-vpn |
|---|---|---|
| Протокол DC (MsgConnect…MsgUDPReply) | `relay/tunnel/protocol.go` | `vk-vpn-client/tunnel/protocol.go` + MsgPing/Pong |
| ChaCha20 obfuscator | join-link token | `tunnel/obfuscator.go` |
| RelayBridge + DCTunnel | joiner/creator | joiner + creator (DC и VP8) |
| VP8DataTunnel + ReadVP8Track | video mode | `tunnel/vp8tunnel.go`, default `VK_VPN_TUNNEL_MODE=video` |
| Resource modes | headless flags | `--resources` на демоне |
| DC backpressure | max-dc-buf | `waitDCBackpressure` |
| DetachDataChannels + SCTP 8MB + zero checksum | pion SettingEngine | joiner + session |
| Bypass до WebRTC | desktoptun | `main.go` + dynamic ICE bypass |
| ICE restart | pion offer | joiner + p2p |
| WS rejoin same call | authAndJoin OKJoinLink | `RejoinConversation` |
| participant-left | не рвёт bridge | bridge.go |

## Отличия (не баг, осознанный выбор)

| Тема | WLB | vk-vpn |
|---|---|---|
| Доставка ссылки | Telegram / S3 / бот | `myvpn://` URI + vk-vpn-bot |
| Desktop TUN | `desktoptun` пакет | `vk-client/wintun` (свой код) |
| Tunnel DC ordered | default ordered | ordered (как WLB tunnel DC) |
| Маскировка IP в логах | `common.MaskAddr` | полные IP (можно добавить) |

## Ещё не перенесено (по приоритету)

### Высокий impact на скорость/стабильность

1. ✅ **VP8 writer pacing WLB** — `keepaliveIdlePeriod`, `currentIntervals`, peer-restart log (`tunnel/vp8tunnel.go`).
2. ⏳ **SOCKS auth на RelayBridge** — поля есть; UI не прокидывает (не throughput).
3. ✅ **ICE relay preference** — `SetRelayAcceptanceMinWait` (default 3s, `VK_VPN_ICE_RELAY_WAIT`), pair log в stats.
4. ✅ **Call rotation** — `--call-ttl` / default 2h на демоне (`daemon.go`).

### Средний

5. **desktoptun: tunnelLost → exit 2 → auto-reconnect** — desktop-joiner перезапускает процесс; Wails-клиент — ручной reconnect.
6. **Pre-resolve bypass hosts** (`resolveHostname` до TUN) — частично: ResolveSession + static vk lists.
7. **VP8 legacy tunnel** — отдельный код path в WLB для старых peers; не нужен если оба на новом obf.
8. ~~Platform matrix~~ — **не в scope**: только VK.

### Низкий / продукт

9. **MaskAddr в логах** — приватность.
10. **Bypass list в join-link payload** — P3 в canvas.
11. **Random WLVPN adapter name** — P3.
12. **Windows watchdog process** — kill -9 cleanup; отложено.

## Тонкости WLB, которые легко пропустить

- **Video mode**: joiner шлёт `EncodeVP8Config` сразу после connect; creator `OnTrack` создаёт symmetric VP8 tunnel — у нас так же.
- **DC mode**: данные только по DC id=2; video tracks — декой для VK.
- **creator OnTrack modeOnce**: первый remote track фиксирует video vs dc — гонка если оба канала оживут; WLB то же.
- **inCh / ch backpressure**: WLB `dcConn.ch` с drop при full; session `inCh` drop log; relay creator пишет в net.Conn напрямую.
- **UDP DNS**: `MsgUDP` на creator только; joiner шлёт через SOCKS UDP associate — проверить что tun2socks UDP доходит.
- **Obf epoch**: смена epoch при peer restart в video — WLB логирует в telemost; vk obfuscator поддерживает Decode epoch.

## Следующий фокус (после throughput)

1. Roadmap: S3 delivery, VK Community bot, comfort-noise audio (backlog).
2. `MaskAddr` в логах (приватность).
3. desktoptun-style auto-reconnect на Windows (tunnelLost).
4. Опционально: MTU 1500, VP8 batch tuning для пиков >20 Mbps.
