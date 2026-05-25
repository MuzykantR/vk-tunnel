# vk-vpn Transport Audit

**Snapshot:** 2026-05-24, after Phase 1 (cleanup) + Phase 3b (KCP-over-VP8) deployed to production.
**Branch:** `feat/transport-rework`.
**Status:** **production state описан** — это не «исторический документ», а описание того, что в коде сейчас.
**Audience:** следующий агент / инженер, который продолжит работу над throughput-оптимизацией.
**Scope:** относится только к `relay` режиму ICE (`VK_VPN_ICE_TRANSPORT_POLICY=relay`) — это жёсткое требование продукта.

---

## 0. TL;DR

- vk-vpn туннелирует SOCKS5/TCP через WebRTC. Контрольная плоскость — DataChannel (SCTP). Полезная нагрузка идёт **через KCP-over-VP8** (Hybrid-B): KCP-протокол с ARQ+FEC поверх обфусцированного «фейкового» VP8 RTP video track. Это default-конфигурация: `VK_VPN_TUNNEL_MODE=kcp`.
- В `relay` режиме весь трафик идёт через **TURN UDP**. SFU **в этом маршруте отсутствует** — TURN это «тупой» UDP-форвардер.
- **Подтверждённый результат:** sustained ~1.2 Mbps, **100% доставка** (даже при 2-12% потерь VP8-сэмплов на физическом уровне — KCP/FEC компенсирует).
- **Не подходит для видеозвонков** на текущем throughput; для текстового чата и базового web — работоспособно.
- Альтернативные режимы остаются как **fallback / для исследований**: `video` (VP8 без ARQ, ненадёжно — теряет 5-15% при потерях), `dc` (SCTP — надёжно, но 53 kbps в relay).
- Lifecycle TCP-соединений — **race-free** после Phase 1 фикса. Стейтфул-фрейминг в RelayBridge — Phase 3b.8.

---

## 1. Высокоуровневая архитектура

```
+--------------------+                         +--------------------+
|  Joiner (клиент)   |                         | Creator (VPS)      |
|                    |                         |                    |
|  vk-vpn-client/    |                         |  vk-vpn-server/    |
|    webrtc/joiner   |                         |    creator/session |
|    tunnel/...      |                         |    creator/bridge  |
+----------+---------+                         +---------+----------+
           |                                             |
           |  1) VK WS (calls.joinByLink → SDP/ICE)      |
           +<-------------------------------------------->+
           |                                             |
           |  2) WebRTC PeerConnection                   |
           |     — DataChannel "tunnel" (negotiated id=2)|
           |     — VP8 video track (носитель KCP)        |
           |     — Audio (Opus, decoy)                   |
           +<===========================================>+
                       (через TURN UDP в relay-режиме)
           ^                                             ^
           |                                             |
+----------+---------+                         +---------+----------+
|  SOCKS5 listener   |                         |  Origin TCP dialer |
|  on 127.0.0.1:<P>  |                         |  net.Dial("tcp",…) |
+--------------------+                         +--------------------+
       ↑                                                  ↓
   curl / browser                                  yandex / google
   (через tun2socks / system proxy)
```

### Path-of-bytes в KCP mode (DL: origin → curl)

1. Origin отдаёт байты по TCP в `vk-vpn-server/creator/session.go:connectTCP` → `conn.Read`.
2. Сервер кадрирует чанк в кадр протокола (`MsgData`, `connID`) и пишет в `kcpTunnel.SendData()`.
3. `KCPTunnel.SendData` → `session.Write(frame)` → KCP packetizer:
   - режет на KCP-сегменты по MTU (1200 байт),
   - добавляет FEC parity (default 10:3),
   - вызывает `vp8PacketConn.WriteTo` для каждого пакета.
4. `vp8PacketConn.WriteTo` → ChaCha20-Poly1305 обфускатор (с fake-VP8 header) → `track.WriteSample(media.Sample{...})`.
5. pion SRTP/DTLS/UDP → TURN.
6. На joiner'е pion → `ReadVP8Track` → `KCPTunnel.HandleFrame(frame)` → obfuscator decode → `vp8PacketConn.deliver(payload)`.
7. KCP read loop в `KCPTunnel.readLoop`: `session.Read(buf)` отдаёт **целый message** (т.к. message mode) → `t.onData(payload)`.
8. `RelayBridge.handleTunnelData(payload)` → **stateful frame parser** копит partial frames в `recvAccum` → парсит целые → `dispatchFrame`.
9. `MsgData` → `socksConn.deliverToApp` → `toApp` chan → `conn.Write` в SOCKS-сокет curl'а.

UL (curl → origin) симметричен: SOCKS read → `RelayBridge.handleSOCKS` → `MsgConnect`/`MsgData` → `kcpTunnel.SendData` → → → server origin write.

---

## 2. Ключевые компоненты

| Файл | Роль |
|------|------|
| `vk-vpn-server/main.go`, `daemon/daemon.go` | Орхестратор VPS. Поднимает creator-bridge, ротирует звонок раз в `--call-ttl`. Принимает `--log-file=PATH` (truncate at start). |
| `vk-vpn-server/signaling/vk.go` | VK API: `calls.start`, `joinConversation`. |
| `vk-vpn-server/creator/bridge.go` | WS-логика VK: handshake, ping, peer-joined, передача SDP/ICE через `transmit-data`. |
| `vk-vpn-server/creator/p2p.go` | DIRECT-only P2P машина. |
| `vk-vpn-server/creator/session.go` | `TunnelSession`: pion PC, DC, VP8 track. **Routing по `VK_VPN_TUNNEL_MODE`**: создаёт `KCPTunnel` (default) либо `VP8DataTunnel` (legacy video) либо использует DC-only path. Содержит DC relay (`connectTCP`, `closeDCConn`) для `mode=dc`. |
| `vk-vpn-client/webrtc/joiner.go` | Клиентский joiner. Аналогичный routing по mode. |
| `vk-client/main.go` | Wails-обёртка. Резолвит mode по приоритету: CLI flag `--tunnel-mode=` → env `VK_VPN_TUNNEL_MODE` → файл `tunnel-mode.txt` рядом с exe → default `video`. |
| `vk-vpn-client/tunnel/protocol.go` | Wire-протокол: `[length:4][connID:4][type:1][payload]`. |
| `vk-vpn-client/tunnel/kcp_tunnel.go` | **KCPTunnel** (Hybrid-B): KCP session over VP8 track. Message mode, FEC 10:3, NoDelay turbo. |
| `vk-vpn-client/tunnel/kcp_packetconn.go` | Адаптер `net.PacketConn` для KCP-go: KCP.Output → obfuscator+WriteSample; VP8 frame → recvCh → KCP.Input. |
| `vk-vpn-client/tunnel/kcp_env.go` | Env-конфиг KCP: `VK_VPN_KCP_FEC`, `_WINDOW`, `_MTU`, `_NODELAY`. |
| `vk-vpn-client/tunnel/dctunnel.go` | DC-транспорт (legacy для `mode=dc`): Detach + readLoop, ChaCha20. |
| `vk-vpn-client/tunnel/vp8tunnel.go` | VP8-транспорт (legacy для `mode=video`): sendQueue (4096), writerLoop с pacing `fps × batch`. **В KCP mode не используется**. |
| `vk-vpn-client/tunnel/vp8read.go` | RTP→VP8 reassembler. Имеет диагностические счётчики (`vp8RTPGapResets` и т.д.) для замера потерь на UDP-пути. |
| `vk-vpn-client/tunnel/obfuscator.go` | ChaCha20-Poly1305 + epoch (4 байта). Используется и KCP, и legacy VP8. |
| `vk-vpn-client/tunnel/relay_bridge.go` | SOCKS5 listener (joiner) и origin TCP dialer (creator). **Stateful frame parser** (`recvAccum`) — устойчив к произвольной фрагментации от любого транспорта. |
| `vk-vpn-client/tunnel/watchdog.go` | MsgPing/Pong раз в 10s, 3 миса → OnUnhealthy → ICE restart (suppress'нут в relay). |
| `vk-vpn-client/webrtc/ice_settings.go` | `ICETransportPolicyFromEnv` — default `relay`. |
| `systemd/vk-vpn-daemon.service` | `Environment=VK_VPN_TUNNEL_MODE=kcp`, `VK_VPN_LOG_FILE=/var/log/vk-vpn-daemon.log`. |

---

## 3. Протокол кадров (wire)

Все кадры внутри tunnel'а одинакового вида:

```
+---------+---------+--------+----------+
| length  | connID  | msgTyp | payload  |
| 4 bytes | 4 bytes | 1 byte |  N bytes |
+---------+---------+--------+----------+
length = 5 + len(payload)
```

`connID = 0` зарезервирован под control (`MsgConfig`, `MsgPing`, `MsgPong`).

### Типы сообщений (`protocol.go`)

| Hex | Имя | Описание |
|-----|-----|----------|
| 0x01 | `MsgConnect` | joiner → creator: «открой TCP к `host:port`» |
| 0x02 | `MsgConnectOK` | creator → joiner: «открыто» |
| 0x03 | `MsgConnectErr` | creator → joiner: ошибка dial |
| 0x04 | `MsgData` | в обе стороны: payload |
| 0x05 | `MsgClose` | в обе стороны: «больше данных не будет» |
| 0x06 | `MsgUDP` | joiner → creator: одиночный UDP-запрос (DNS) |
| 0x07 | `MsgUDPReply` | creator → joiner: UDP-ответ |
| 0x08 | `MsgConfig` | joiner → creator: согласование `fps/batch` для VP8 (legacy, в KCP-mode no-op) |
| 0x09 | `MsgPing` | watchdog probe |
| 0x0A | `MsgPong` | watchdog reply |

Поверх KCP/DC ChaCha20 шифрует payload. KCP-mode дополнительно вшивает 17/20-байтный fake VP8 header + 4 байта epoch.

---

## 4. KCP-over-VP8 mode (default, Hybrid-B)

`VK_VPN_TUNNEL_MODE=kcp`. Работающий режим в проде.

### Архитектура

```
RelayBridge.SendData(frame)
        ↓
KCPTunnel.SendData
        ↓ session.Write(frame)             ← KCP в message mode: 1 Write = 1 message
KCP segmentizer (kcp-go/v5)
        ↓ split в KCP-сегменты MTU 1200
        ↓ append FEC parity (default 10:3 → +30%)
        ↓ Output callback
vp8PacketConn.WriteTo(packet)
        ↓ obfuscator.EncodeData(packet)    ← ChaCha20 + fake VP8 header
        ↓ track.WriteSample(media.Sample{Duration: 33ms})
pion SRTP/DTLS/UDP → TURN
```

На приёме симметрично: `ReadVP8Track → KCPTunnel.HandleFrame → obfuscator.Decode → vp8PacketConn.deliver → KCP.Input → session.Read → onData(frame)`.

### Ключевые свойства

- **Reliable**: KCP делает Selective Repeat ARQ + опциональный Reed-Solomon FEC. Каждый потерянный/повреждённый сегмент восстанавливается.
- **Stream-as-frame**: `SetStreamMode(false)` — каждый `Write()` отправителя == один `Read()` получателя. Граница frame'ов wire-протокола сохраняется.
- **Cover traffic**: VP8-keepalive отдельной горутиной каждые 100 ms — VK TURN видит «активный звонок».
- **Bound queues**: `vp8PacketRecvCap = 1024 packets` recv-queue, KCP-window `2048` (env-tunable).
- **Метрики**: `kcp-rate` каждые 10 сек: `in / out / Δ / Mbps / srtt / rto / recv_drop`.

### Подтверждённые цифры (24.05.2026)

| Метрика | Значение |
|---------|----------|
| Sustained throughput | ~1.2 Mbps (≈150 KB/s) |
| Burst | до 2.3 Mbps |
| Delivery integrity | 100% (server.rx=96331062 ↔ client KCP cumulative совпадают) |
| VP8 physical loss | 2-12% (variable) |
| App-level loss | 0% |
| SRTT | 60-90 ms |
| RTO | 70-200 ms |

### Зачем VP8 в качестве носителя

В **relay-only** на TURN'е SFU отсутствует — поэтому DPI-аргумент против DC не работает на TURN. Но **на пути от клиента до TURN'а** есть DPI и observer'ы на самом TURN-сервере VK. Они видят:

- `video=keepalive + DC=18 Mbps` → подозрительно (звонок без видео-потока).
- `video=1-2 Mbps + DC=0-10 kbps` → нормальный звонок.

KCP-over-VP8 даёт второй профиль: трафик идёт через video track в SRTP-конверте, snimки DPI видят регулярный VP8-битрейт.

### Что **не делает** этот режим

- Не выжимает throughput. KCP конкурирует с FEC overhead + UDP loss. Sustained 1.2 Mbps это **сейчас**, не «предел физики».
- Не bonding'ует несколько каналов.
- Не оптимизирован под HoL — все TCP-flows конкурируют в одном KCP-stream'е.

---

## 5. Legacy `video` mode (VP8 без ARQ)

`VK_VPN_TUNNEL_MODE=video`. **Не рекомендуется** для прода — теряет данные.

- `VP8DataTunnel`: sendQueue 4096, writerLoop с pacing `fps × batch` (default 24×30).
- Receiver (`ReadVP8Track`): hard-reset кадра при любом RTP seq gap.
- Любая потеря 1 RTP-пакета = потеря всего VP8-сэмпла = потеря N frames протокола внутри.
- Подтверждено: gap_resets 8-35% на лоссивом TURN → curl 84% → `RST`.

Оставлен для:
1. A/B-сравнений throughput против KCP-mode.
2. Fallback если KCP в каком-то будущем сценарии сломается.

---

## 6. Legacy `dc` mode (SCTP only)

`VK_VPN_TUNNEL_MODE=dc`. **Не рекомендуется** для bulk — слишком медленно в relay.

- `DCTunnel`: Detach + readLoop, payload через `raw.Write`.
- SCTP делает свою reliable+ordered+CC.
- VP8-track всё ещё создан, но в него **ничего не пишут** (нет cover-трафика).

Подтверждено в замерах:
- Sustained ~53 kbps в relay через VK TURN.
- 100% доставка.
- SCTP-CC слишком консервативен на лоссивых каналах: на каждую потерю режет cwnd, не восстанавливается достаточно быстро.

Оставлен для диагностики (чистая SCTP-доставка без VP8-носителя).

---

## 7. Lifecycle TCP-соединения

### Joiner side (`relay_bridge.go::handleSOCKS`)

1. App открывает TCP к `127.0.0.1:<port>`.
2. SOCKS5 handshake.
3. `id := rb.nextID.Add(1)`, создаётся `socksConn{id, conn, rdy, toApp, doneCh, closeOnce}`.
4. `sc.initAppWriter()` запускает goroutine: `appWriterLoop` дренирует `sc.toApp` в `conn.Write`. На любом exit → `defer sc.close()` через `closeOnce`.
5. `rb.conns.Store(id, sc)`, `rb.send(id, MsgConnect, host)`.
6. Ждём `MsgConnectOK` или `MsgConnectErr` (timeout 20s).
7. Если OK — `conn.Write(socks.OK)`, запускаем reader-goroutine: `for { n, err := conn.Read(buf); rb.send(id, MsgData, buf[:n]) }`.
8. На входящий `MsgData` через KCP → stateful frame parser → `sc.deliverToApp(payload)` (через `select{toApp|doneCh}`, никогда не паникует).
9. На входящий `MsgClose` → `closeJoinerAfterInboundDrain(sc, connID)`: ждёт inbound drain до tail-grace, потом `rb.conns.Delete(connID); sc.close()`.

### Creator side (`session.go::connectTCP`)

1. `MsgConnect("host:port")` → `net.DialTimeout("tcp", addr, 10s)`.
2. `rb.send(id, MsgConnectOK, nil)` (или `MsgConnectErr`).
3. Параллельные goroutines:
   - `inCh` writer: `for data := range dc.inCh { conn.Write(data); dc.tx.Add(n) }`.
   - read loop: `conn.Read(buf) → dc.rx.Add(n); s.sendDCFrame(id, MsgData, buf[:n])`.
4. На входящий `MsgData` от joiner → `dc.inCh <- cp` через `select{inCh|doneCh}` — backpressure через SCTP/KCP, **silent drop устранён**.
5. EOF от origin → **TCP half-close**:
   - `s.sendDCFrame(connID, MsgClose, nil)` (announce joiner'у),
   - `tcpConn.CloseWrite()` (FIN origin'у — он флашит остатки и ACK'ает),
   - polling-wait joiner'ского `MsgClose` или `RelayDrainTimeoutFromEnv()` (default 120s),
   - `s.closeDCConn(connID)` (idempotent через `sync.Once`).
6. На входящий `MsgClose` от joiner → тот же `closeDCConn(connID)`.

---

## 8. Bugs (status: все §7 из прошлой версии аудита **исправлены** в Phase 1+3b)

| # | Описание | Status | Где |
|---|----------|--------|-----|
| 7.1 | Race на `socksConn.toApp` (send on closed channel) | ✅ FIXED | doneCh + closeOnce паттерн |
| 7.2 | `closeDCConn` маска `defer recover()` | ✅ FIXED | `sync.Once` в `dc.close()` |
| 7.3 | Нет TCP half-close на creator | ✅ FIXED | `CloseWrite()` + polling-drain |
| 7.4 | `dcConn.inCh` silent drop | ✅ FIXED | blocking `select{inCh|doneCh}` |
| 7.5 | `closeRelayTCP` всегда `tx=0` | ✅ FIXED | atomic `relayTCP.tx` |
| 7.6 | VP8 reassembler без jitter buffer | ⚠️ ОСТАЁТСЯ | **не критично в KCP mode** — KCP сам restransmit'ит. В legacy `video` mode проблема не решена. |
| 7.7 | `startAppWriter` через `sync.Once` (не recoverable) | ✅ FIXED | `initAppWriter` + idempotent close |
| 3b.6 | KCP readLoop killed by read timeout (`isTimeoutErr` не распознавал) | ✅ FIXED | blocking Read, exit only on Close |
| 3b.8 | DecodeFrames stateless → теряет partial frames на стороне receiver | ✅ FIXED | stateful `recvAccum` в RelayBridge |

---

## 9. ICE-нюансы (relay-only) — без изменений

- `VK_VPN_ICE_TRANSPORT_POLICY=relay` — default в коде.
- `SetRelayAcceptanceMinWait` отключён в relay-only.
- TURN URL берётся из VK signaling response.
- Watchdog ICE-restart подавлен в relay-режиме.
- При relay-only **обязателен** `VK_VPN_EXTRA_BYPASS_IPS=<VPS IP>` иначе SSH/admin-трафик к VPS уйдёт внутрь туннеля.

---

## 10. Чего в коде сейчас НЕТ

- **Bonding** (несколько KCP-stream'ов / video-track'ов на одну сессию). Hybrid-C из старого плана.
- **Адаптивный FEC** (выключать когда канал чистый, включать при потерях).
- **TURN-server selection** (выбираем тот, что VK отдал; нет возможности перебрать).
- **Jitter buffer** в legacy VP8 mode — не нужно (legacy).
- **HTTP/2 multiplexing-aware proxy** — каждый TCP идёт отдельным connID, multiplex не используется.

---

## 11. Что есть и работает (не ломать)

- **Obfuscator с epoch'ом** — корректно детектит peer restart.
- **`Detach()` + raw read loop** на DC.
- **`SetSCTPMaxReceiveBufferSize(8 MB)` + `EnableSCTPZeroChecksum`**.
- **VK-WS signaling, P2P машина, ICE bypass для TURN/VPS IP**.
- **Watchdog с suppress'ом ICE-restart на relay**.
- **KCP message-mode + stateful framing в RelayBridge** — гарантирует доставку frames независимо от транспорта.
- **TCP half-close на creator** — хвост больших файлов больше не теряется.
- **Idempotent close lifecycle** — нет паник на race.
- **File-backed log сервера** с auto-truncate на restart.
- **CLI/file/env-резолвер mode** в Windows-клиенте.

---

## 12. Окружение, env-переменные, флаги

### Сервер

| Env | Default | Назначение |
|-----|---------|------------|
| `VK_VPN_TUNNEL_MODE` | `kcp` (через systemd unit) | `kcp` / `video` / `dc` |
| `VK_VPN_LOG_FILE` | `/var/log/vk-vpn-daemon.log` | Файл для лога (truncate at start) |
| `VK_VPN_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |

CLI: `--cookies`, `--port`, `--resources=moderate|default|unlimited|custom`, `--call-ttl`, `--log-file`.

### Клиент

| Источник | Приоритет | Пример |
|----------|-----------|--------|
| CLI flag | 1 | `vk-client.exe --tunnel-mode=kcp` |
| Env | 2 | `$env:VK_VPN_TUNNEL_MODE = "kcp"` (PowerShell same session) |
| File | 3 | `C:\VPN\tunnel-mode.txt` рядом с exe |
| Default | 4 | `video` |

| Env | Default | Назначение |
|-----|---------|------------|
| `VK_VPN_TUNNEL_MODE` | `video` | См. выше |
| `VK_VPN_ICE_TRANSPORT_POLICY` | `relay` | TURN-only |
| `VK_VPN_KCP_FEC` | `10:3` | FEC data:parity или `off` |
| `VK_VPN_KCP_WINDOW` | `2048` | KCP send/recv window (packets) |
| `VK_VPN_KCP_MTU` | `1200` | KCP MTU (с учётом fake-VP8+obf+DTLS overhead) |
| `VK_VPN_KCP_NODELAY` | `1,10,2,1` | `nodelay,interval_ms,resend,nocongestion` |
| `VK_VPN_VP8_FPS` / `VK_VPN_VP8_BATCH` | 24 / 30 | Legacy VP8 pacing |
| `VK_VPN_VP8_SEND_QUEUE` | 4096 | Legacy VP8 sendQueue depth |
| `VK_VPN_RELAY_DRAIN_TIMEOUT` | 120 s | Дренаж перед `MsgClose` |
| `VK_VPN_RELAY_INBOUND_GRACE_SEC` | 10 s | Joiner ждёт хвост после `MsgClose` |
| `VK_VPN_RELAY_INBOUND_IDLE_MS` | 500 ms | Тихий период перед close TCP |
| `VK_VPN_RELAY_CONNECT_LIMIT` | 0 (off) | Cap на parallel CONNECT |
| `VK_VPN_EXTRA_BYPASS_IPS` | — | Обязательно для SSH к VPS в relay |

---

## 13. Где курить дальше

Цель доставки достигнута. Следующая цель — **throughput**. См. сопровождающий план: `docs/THROUGHPUT_PLAN.md`.

Стартовая точка: 1.2 Mbps sustained. Целевая планка: 5+ Mbps (минимум для нормального веб-серфинга), стрейч: 10 Mbps.
