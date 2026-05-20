# Implementation Plan — статус и следующие шаги

> Обновлено: **20 мая 2026** — prod verify: VP8 ~18/17 Mbps, ICE стабилен. Checkpoint: `canvases/vk-vpn-audit.canvas.tsx`, бенчмарки: [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

Полная карта: [WLB_INTEGRATION.md](WLB_INTEGRATION.md) (все `.md` из whitelist-bypass).

## Prod verify (20.05.2026) ✅

| Проверка | Результат |
|----------|-----------|
| VP8 video mode | `[creator] === MODE: VIDEO (VP8) ===`, config fps=24 batch=30 |
| ICE стабильность | connected, **без** disconnect/restart под нагрузкой |
| Speedtest | DL **18.28** / UL **17.37** Mbps, ping 107 ms (351 ms под load) |
| Практика | 4K streaming OK; пик speedtest ~50 Mbps → спад |

См. [docs/BENCHMARKS.md](docs/BENCHMARKS.md).

---

## Что уже реализовано (Фаза 0 + 1.5 + WLB A–C)

### Стабильность подключения

- ✅ **tun2socks + bypass routes ДО WebRTC** (`vk-client/main.go`)
- ✅ **Ожидание ICE stable 3 s** перед default route, **без** 6 s fallback
- ✅ **Динамический bypass ICE**, flush перед redirect
- ✅ **ICE restart** (grace 12 s, suppress 45 s после first connect)
- ✅ **RejoinConversation** — один звонок, `--call-ttl` 2h rotation
- ✅ Cleanup: `addedRoutes`, `RemoveAdapter`, `Get-NetRoute` gateway

### WLB интеграция

- ✅ Фаза A: obf, resources, DC backpressure, UDP relay, codecs
- ✅ Фаза B: `DCTunnel` + `RelayBridge` на joiner
- ✅ **Фаза C: VP8 video mode** — default, **проверено в prod ~18 Mbps**
- ✅ VP8 pacing WLB (`keepaliveIdlePeriod`, `currentIntervals`)
- ✅ `SetRelayAcceptanceMinWait`, ICE pair log (`icepair.go`)
- ✅ logx, MsgPing/Pong watchdog, `MASTER_KEY` env

---

## Известные проблемы (актуально)

### Закрыто

- ~~ICE disconnect через 6 s~~ — исправлено, prod OK
- ~~Скорость 2–11 Mbps~~ — VP8 prod **~18 Mbps**
- ~~VP8 не проверен~~ — verify 20.05.2026

### Открыто (низкий приоритет)

1. **Speedtest.net** — может отличаться от fast.com (WebSocket/H2); перепроверить при необходимости.
2. **Пик 50 Mbps → спад** — нормально при burst + relay RTT; не баг.
3. **Watchdog Windows** — только hard crash; cleanup при Disconnect достаточен.
4. **SOCKS auth UI** — поля есть, Wails не прокидывает.

---

## Следующие шаги (продукт, не throughput)

| Приоритет | Задача |
|-----------|--------|
| P2 | Delivery ссылок через Yandex S3 ([roadmap.md](roadmap.md) фаза 2) |
| P2 | VK Community Bot вместо cookies (фаза 4) |
| P3 | Comfort-noise / synthetic audio на track (antifraud, backlog) |
| P3 | MTU 1500 A/B vs 1400 (мелкий тюнинг) |
| P3 | MaskAddr в логах |

Throughput **одного звонка** для MVP — **достаточен**. Дальше — стабильность продукта и доставка ссылок.

### Фаза 0 — измерения ✅ (20.05.2026)

- Итог: [docs/BENCHMARKS.md](docs/BENCHMARKS.md) — линия 72/70 (Яндекс), VPN ~15 DL / 40 UL (Ookla), **host/host**, curl tail stall @96%
- Протокол: [docs/BENCHMARK_PROTOCOL.md](docs/BENCHMARK_PROTOCOL.md)

### Фаза 1 — throughput (код) ✅

- VP8: burst drain до `batch` samples/tick; non-blocking `SendData` + drop counter; large-frame logs; `VK_VPN_VP8_SEND_QUEUE`, `VK_VPN_VP8_PROFILE=aggressive`
- ICE: relay warn; повторный `ICE selected` через 30s
- MTU: `VK_VPN_MTU` в wintun + tun2socks

**Проверка после деплоя:** baseline 24×30, затем `VK_VPN_VP8_PROFILE=aggressive`, повторить Ookla + curl mirror.yandex.ru; записать в §9 THROUGHPUT_HYPOTHESES.

---

## Файлы (ключевые)

| Файл | Роль |
|------|------|
| `vk-client/main.go` | Connect: tun → ICE stable → redirect |
| `vk-vpn-client/webrtc/joiner.go` | VP8/DC, ICE, bypass |
| `vk-vpn-server/creator/session.go` | Creator VP8 OnTrack + relay |
| `vk-vpn-client/tunnel/vp8tunnel.go` | VP8 pacing (WLB) |
| `docs/LOGGING.md` | Формат логов + VP8 frame sizes |
| `docs/WLB_GAP_ANALYSIS.md` | Пробелы vs WLB |
