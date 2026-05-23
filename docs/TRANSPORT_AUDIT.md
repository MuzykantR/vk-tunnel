# vk-vpn Transport Audit (snapshot at commit `fb37254` — branch `feat/transport-rework`)

**Status:** audit only, **no code changes**.
**Audience:** следующий агент / инженер, который продолжит работу над транспортом без прочтения других документов проекта.
**Scope:** относится только к `relay` режиму ICE (`VK_VPN_ICE_TRANSPORT_POLICY=relay`) — это жёсткое требование продукта.

---

## 0. TL;DR

- vk-vpn туннелирует SOCKS5/TCP через WebRTC. Контрольная плоскость — DataChannel (SCTP). Полезная нагрузка — *одна из двух*: либо тот же DataChannel (DC mode), либо «фейковый» VP8 RTP video track (video mode, **default**).
- В `relay` режиме весь трафик идёт через **TURN UDP** (или TURN TCP, если pion выберет TCP-кандидата). SFU **в этом маршруте отсутствует** — TURN это «тупой» UDP-форвардер. Любые рассуждения «VK SFU режет битрейт» **не применимы** к нашему сценарию.
- VP8 mode по сути — это самопальный «UDP-тоннель без ретрансмитов»: любая потеря RTP-пакета приводит к потере целого VP8-сэмпла (= 1 порция SOCKS-данных размером до `readBuf` ≈ 32 KB). Это и есть причина «curl 84% → connection reset».
- DC mode — это полноценный SCTP с надёжной упорядоченной доставкой, congestion control и flow control. **В relay-режиме у него нет недостатков, кроме непротестированной throughput-характеристики**.
- В коде есть **реальные баги lifecycle** (`socksConn.toApp` race, server-side double-close inCh, отсутствие half-close), которые провоцируют отдельный класс «обрывов» — независимо от потерь UDP.

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
           |     — VP8 video track                       |
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

### Конкретный path-of-bytes (DL: origin → curl)

1. Origin отдаёт байты по TCP в `vk-vpn-server/creator/session.go:connectTCP` → `conn.Read`.
2. Сервер заворачивает чанк в кадр протокола (`MsgData`, `connID`) и кладёт в outbound — либо `s.sendDCFrame()` (DC mode) либо через `vp8Tunnel.SendData()` (video mode).
3. В video mode: `VP8DataTunnel.writerLoop` собирает payload, обфусцирует ChaCha20 + epoch-header, отдаёт на pion track как **один VP8 sample**.
4. pion разбивает sample на RTP-пакеты (MTU ~1400), шлёт через SRTP/DTLS/UDP в TURN.
5. TURN форвардит UDP-пакеты joiner'у.
6. На joiner'е pion собирает RTP → `ReadVP8Track` склеивает обратно VP8 sample → `VP8DataTunnel.HandleFrame` → ChaCha20 decode → `RelayBridge.handleTunnelData` → парсинг кадров → `socksConn.deliverToApp` → `conn.Write` в SOCKS-сокет curl'а.

### Path-of-bytes (UL: curl → origin) — симметрично

curl → SOCKS5 → joiner `RelayBridge.handleSOCKS` → `MsgConnect`/`MsgData` → VP8 sample → TURN → creator → `connectTCP` → `conn.Write` → origin.

---

## 2. Ключевые компоненты

| Файл | Роль |
|------|------|
| `vk-vpn-server/main.go`, `daemon/daemon.go` | Орхестратор VPS. Поднимает creator-bridge, ротирует звонок раз в `--call-ttl`. |
| `vk-vpn-server/signaling/vk.go` | VK API: `calls.start`, `joinConversation`. Получает WS endpoint и TURN credentials. |
| `vk-vpn-server/creator/bridge.go` | WS-логика VK: handshake, ping, peer-joined, передача SDP/ICE через `transmit-data`. |
| `vk-vpn-server/creator/p2p.go` | DIRECT-only P2P машина: формирует offer, принимает answer, обрабатывает кандидатов. |
| `vk-vpn-server/creator/session.go` | `TunnelSession`: pion PC, DC, VP8 track. DC relay (`connectTCP`, `closeDCConn`). |
| `vk-vpn-client/webrtc/joiner.go` | Клиентский joiner: WS, ICE, PC, dc/vp8 tunnel, bypass-логика для роутинга. |
| `vk-vpn-client/tunnel/protocol.go` | Wire-протокол: `[length:4][connID:4][type:1][payload]`. |
| `vk-vpn-client/tunnel/dctunnel.go` | DC-транспорт: Detach + readLoop, ChaCha20. |
| `vk-vpn-client/tunnel/vp8tunnel.go` | VP8-транспорт: sendQueue (4096), writerLoop с pacing `fps × batch`. |
| `vk-vpn-client/tunnel/vp8read.go` | RTP→VP8 reassembler. **Жёсткий reset кадра при любом seq gap**. |
| `vk-vpn-client/tunnel/obfuscator.go` | ChaCha20-Poly1305 + epoch (4 байта) для детекта peer restart. |
| `vk-vpn-client/tunnel/relay_bridge.go` | SOCKS5 listener (joiner) и origin TCP dialer (creator). Общая FSM по кадрам. |
| `vk-vpn-client/tunnel/watchdog.go` | MsgPing/Pong раз в 10s, 3 миса → OnUnhealthy → потенциальный ICE restart. |
| `vk-vpn-client/webrtc/ice_settings.go` | `ICETransportPolicyFromEnv` — default `relay` (для тестов whitelist). |

---

## 3. Протокол кадров (wire)

Все кадры (и в DC, и в VP8) одинакового вида:

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
| 0x08 | `MsgConfig` | joiner → creator: согласование `fps/batch` для VP8 |
| 0x09 | `MsgPing` | watchdog probe |
| 0x0A | `MsgPong` | watchdog reply |

**Особенности:**

- Поверх SCTP DC ChaCha20 шифрует только plaintext payload (тонкий nonce-блок). VP8 mode дополнительно вшивает 17/20-байтный fake VP8 header + 4 байта epoch — чтобы пакеты «выглядели как VP8 frame». Это обфускация, а не VP8-encoder: реального VP8 кодирования нет.
- `MsgConfig` отправляет joiner раз при создании tunnel'а (`startVideoTunnel`), creator применяет `Reconfigure(fps, batch)` к своему `VP8DataTunnel`.

---

## 4. VP8-mode под микроскопом

### 4.1 Sender (joiner side и creator side зеркальны)

`VP8DataTunnel.SendData(data)`:

```go
payload := append-copy
for {
    select {
    case t.sendQueue <- payload:   // depth=4096 по умолчанию
        return
    case <-t.stopCh:
        return
    }
}
```

`writerLoop`:

```text
каждый tick (sampleInterval = (1s / fps) / batch):
  если в очереди есть данные — взять до batch штук, обфусцировать, t.track.WriteSample(...)
  если очередь пуста idleTicks подряд (100 ms / sampleInterval) — отправить keepalive sample
```

Backlog drain: если `pending > batch*2`, то `maxBurst = min(batch*8, pending, 512)`.

**Backpressure:** SendData блокирует, пока место не появится в очереди → relay TCP read loop замирает → ОС-TCP к origin сама замедляется. Это работает, но **только в направлении joiner→creator**. В обратку backpressure нет (см. §4.3).

### 4.2 Receiver (`vp8read.go::ReadVP8Track`)

```go
for каждого RTP-пакета:
  если seq != lastSeq + 1:
    frameValid = false
    frameBuf = []         // ВЫБРАСЫВАЕМ всё, что собрали в этом кадре
    continue
  …
  если pkt.Marker: handler(frameBuf); reset
```

**Это самое слабое место.**

- Один потерянный RTP-пакет = потеря **всего** VP8-сэмпла = потеря **N кадров обфусцированного протокола внутри**.
- Никакого jitter-буфера. На любой `gap` (даже out-of-order не из-за потери, а из-за переупорядочения TURN'ом) кадр уходит в мусор.
- VP8 sample может содержать payload до `readBuf` (32 KB по `default`, 12 KB по `unlimited`). 12 KB на MTU 1400 = ~9 RTP-пакетов на sample. Lose 1 → lose 12 KB полезной TCP-нагрузки.

**Последствия:** в VPN-канале появляются «дырки» в потоке байт SOCKS, TLS-стрим выше получает разрыв континуума → `MAC error` → curl видит `RST` → 84 % download.

### 4.3 Полное отсутствие app-level reliability

- Нет sequence number в payload (`MsgData` не нумерован, только connID).
- Нет ACK/NACK.
- Watchdog (`MsgPing/Pong`) видит только живой/неживой канал, а не дыры.
- Receiver, потеряв sample, ничего никому не говорит.
- TCP в curl-side и origin-side **не могут договориться** через нашу прослойку: они оба видят только концы трубы, а потери в середине невидимы для них.

---

## 5. DC-mode под микроскопом

`VK_VPN_TUNNEL_MODE=dc`.

- Использует тот же negotiated DataChannel `id=2`, что в video-режиме служит control plane'ом.
- `DCTunnel.SendData` пишет напрямую в `raw.Write` (detached). SCTP сам фрагментирует, делает retransmit, ordering.
- Backpressure: `waitDCBackpressure` следит за `dc.BufferedAmount()` и тормозит relay-read loop при превышении `MaxDCBuf`.
- ChaCha20 шифрование внутри `EncryptPayload/DecryptPayload`. Без VP8-обфускации.

**Что это даёт в relay-режиме:**

- Reliable ordered delivery — гарантия от SCTP, бесплатно.
- Congestion control — SCTP-CC встроен в pion.
- Flow control — встроен.
- TURN UDP — тот же, что и под VP8 (через DTLS+SCTP), т.е. сам по себе путь идентичный.

**Что НЕ даёт:**

- Маскировка «звонок выглядит активным»: при DC mode video-track не используется для bulk, только audio + keepalives. VK SFU может flag'нуть «звонок без видеотрафика», но **в TURN-relay SFU отсутствует**, поэтому это нерелевантно.
- Throughput на пути с SFU/policer'ом теоретически ниже, чем VP8 (если SFU режет non-media). **В relay-only — этого фактора нет**.

---

## 6. Lifecycle TCP-соединения (DL example)

### Joiner side (`relay_bridge.go::handleSOCKS`)

1. App открывает TCP к `127.0.0.1:<port>`.
2. SOCKS5 handshake (`socks.NegotiateAuth`, `socks.ParseAddress`).
3. `id := rb.nextID.Add(1)`, создаётся `socksConn{id, conn, rdy, toApp}`.
4. `sc.startAppWriter()` запускает горутину, которая льёт из `sc.toApp` chan в `conn.Write`.
5. `rb.conns.Store(id, sc)`, `rb.send(id, MsgConnect, host)`.
6. Ждём `MsgConnectOK` или `MsgConnectErr` (timeout 20s).
7. Если OK — `conn.Write(socks.OK)`, запускаем второй goroutine: `for { n, err := conn.Read(buf); rb.send(id, MsgData, buf[:n]) }`.
8. На входящий `MsgData` — `sc.deliverToApp(payload)` → пушим в `toApp` chan.
9. На входящий `MsgClose` → `closeJoinerAfterInboundDrain(sc, connID)`.

### Creator side (`relay_bridge.go::connectTCP`) для video mode / (`session.go::connectTCP` для DC mode)

1. `MsgConnect("host:port")` → `net.DialTimeout("tcp", addr, 10s)`.
2. `rb.send(id, MsgConnectOK, nil)` (или `MsgConnectErr`).
3. `for { n, err := conn.Read(buf); rb.send(id, MsgData, buf[:n]) }`.
4. На входящий `MsgData` — `conn.Write(payload)`.
5. EOF от origin → `WaitOutboundDrain(120s)` → `rb.send(id, MsgClose, nil)` → `closeRelayTCP(id)` → `conn.Close()`.
6. На входящий `MsgClose` от joiner → `closeRelayTCP(id)` → `conn.Close()`.

---

## 7. Найденные баги (подтверждено чтением кода в `fb37254`)

### 7.1 **Race на `socksConn.toApp` при close** ⚠️ КРИТИЧНО

`sc.stopAppWriter()` делает:

```go
close(sc.toApp)
sc.toApp = nil
```

`sc.deliverToApp(payload)`:

```go
sc.startAppWriter()                  // sync.Once — не пересоздаст после Stop
p := copy(payload)
select { case sc.toApp <- p:
default:  sc.toApp <- p  /* блокируем намеренно */ }
```

**Проблема:**

- `stopAppWriter` вызывается из `closeJoinerAfterInboundDrain` (по `MsgClose`) и из reader-goroutine `handleSOCKS` (по `Read` error).
- Между `stopAppWriter()` и `rb.conns.Delete(id)` есть окно (~ns), в котором ещё один `MsgData` для этого `connID` может прийти и пройти через `rb.conns.Load(connID, ok=true)` → `deliverToApp` → `sc.toApp <- p`.
- `toApp` уже **закрыт** → `panic: send on closed channel`.
- Альтернативный путь: `toApp = nil` → `nil channel send` → горутина handleTunnelData блокируется навечно.

**Симптом, который пользователь и предыдущий агент уже видели:** `conn.Close()`-связанные «обрывы». Воспроизводится при быстрых close-from-server одновременно с inflight трафиком.

### 7.2 **Server-side `dc.inCh` double-close замаскирован recover()**

`session.go::closeDCConn`:

```go
func() {
    defer func() { recover() }()
    close(dc.inCh)
}()
dc.conn.Close()
```

`defer recover()` — это не fix, это маска. Подлежащий race: `closeDCConn` вызывается из (a) `MsgClose` handler'а, (b) ошибки в `connectTCP` read loop, (c) `closeAllConns` при `Close()`. Все три могут пересечься. Должно быть через `sync.Once` или атомарный флаг.

### 7.3 **Нет TCP half-close на creator**

`connectTCP` (и `session.go`, и `relay_bridge.go`):

```go
for {
    n, err := conn.Read(buf)
    ...
    if err != nil { break }
}
rb.send(id, MsgClose, nil)
rb.closeRelayTCP(id)   // → conn.Close()
```

На EOF от origin **сразу** уничтожается весь сокет (`Close()`, не `CloseWrite()`). Если в этот момент joiner ещё что-то пишет в creator → server потеряет последние байты и/или origin не получит финальный TCP-ACK на свои FIN'ы. Это — известная причина «хвостового stall» на больших файлах (curl 99 %, потом обрыв).

Корректное поведение: `conn.(*net.TCPConn).CloseWrite()` после EOF чтения, дренаж inbound `MsgData` до `MsgClose` от joiner (или короткий timeout), потом `Close()`.

### 7.4 **`dcConn.inCh` drop вместо backpressure**

`session.go::handleDCMessage`, case `MsgData`:

```go
select { case dc.inCh <- cp:
default: log.Printf("[dc] conn %d inCh full, dropping %d bytes from joiner", ...) }
```

**Потеря байтов upstream** (curl → origin) при перегрузе! Лечится либо `dc.inCh <- cp` без default (backpressure на DC), либо переходом на прямую запись в `conn.Write`. Сейчас — silent data loss.

### 7.5 **`closeRelayTCP` всегда логирует `tx=0`**

`relayTCP` хранит только `rx int64`. Нет счётчика записанных байт на origin. Это не баг работы, но диагностический шум: в логе всегда `tx=0 rx=N`, что мешает анализу.

### 7.6 **VP8 reassembler не имеет jitter buffer**

См. §4.2 — любой RTP gap = выкинутый sample.

### 7.7 **`startAppWriter` использует `sync.Once`**

После `stopAppWriter` восстановить goroutine невозможно. Сейчас это «безопасно» только потому, что после `stop` уже нет данных. Но это хрупко и провоцирует §7.1.

---

## 8. ICE-нюансы (relay-only)

- `VK_VPN_ICE_TRANSPORT_POLICY=relay` — default в коде, pion использует ТОЛЬКО TURN-кандидаты (host/srflx/prflx отфильтрованы).
- `SetRelayAcceptanceMinWait` отключён в relay-only (бессмысленно — нет direct'ов, которые надо ждать).
- TURN URL берётся из VK signaling response (`turn_server.urls`), TURN-TCP подмешан как fallback (`?transport=tcp`).
- Watchdog ICE-restart **подавлен** в relay-режиме (`SelectedICEPairUsesRelay` → return), потому что restart на TURN-relay редко помогает.
- При relay-only **обязателен** `VK_VPN_EXTRA_BYPASS_IPS=<VPS IP>` иначе SSH/admin-трафик к VPS уйдёт внутрь туннеля → broken pipe (см. `RELAY_BENCHMARK.md` §SSH).

---

## 9. Чего в коде сейчас НЕТ (важно для следующего агента)

- **ARQ.** Никакого. `arq.go` не существует в `fb37254`. Все попытки (Phase 3.1 – 4 в `AGENTS_LOG.md`) были после этого коммита и были откатаны / признаны неудачными.
- **FEC.** Никакого.
- **Sequence numbering** на уровне `MsgData`. Нумеруются только RTP-пакеты pion'ом — но это уровень транспорта, не приложения.
- **Cumulative ACK / NACK.** Никакого.
- **Jitter buffer** на receive side. Нет.
- **Half-close на сервере.** Нет.

---

## 10. Что есть и работает (не ломать без необходимости)

- **Obfuscator с epoch'ом** — корректно детектит peer restart (joiner может перезайти, creator увидит epoch-mismatch и сбросит свой VP8-state).
- **Backpressure joiner→creator через `SendData` блокировку** — корректный механизм flow control в одну сторону.
- **`Detach()` + raw read loop** на обеих сторонах DC — снимает блокировку SCTP reader'а при медленном consumer'е.
- **`SetSCTPMaxReceiveBufferSize(8 MB)` + `EnableSCTPZeroChecksum`** — корректные тюнинги под high-BDP.
- **VK-WS signaling, P2P машина, ICE bypass для TURN/VPS IP** — стабильно работают.
- **Watchdog с suppress'ом ICE-restart на relay** — корректное решение (restart редко лечит TURN-проблемы).

---

## 11. Окружение, env-переменные, флаги

См. `LOGGING.md` и `RELAY_BENCHMARK.md`. Ключевые для транспорта:

| Env | Default | Влияет на |
|-----|---------|-----------|
| `VK_VPN_TUNNEL_MODE` | `video` | `video` = VP8-bulk, `dc` = SCTP-bulk |
| `VK_VPN_ICE_TRANSPORT_POLICY` | `relay` | TURN-only (наше требование) |
| `VK_VPN_VP8_FPS` / `VK_VPN_VP8_BATCH` | 24 / 30 | Pacing VP8 writerLoop |
| `VK_VPN_VP8_SEND_QUEUE` | 4096 | Глубина sendQueue |
| `VK_VPN_RELAY_DRAIN_TIMEOUT` | 120 s | Дренаж sendQueue перед `MsgClose` |
| `VK_VPN_RELAY_INBOUND_GRACE_SEC` | 10 s | Joiner ждёт хвост данных после `MsgClose` |
| `VK_VPN_RELAY_INBOUND_IDLE_MS` | 500 ms | Тихий период перед close TCP |
| `VK_VPN_RELAY_CONNECT_LIMIT` | 0 (off) | Cap на parallel CONNECT |

Сервер: `--resources moderate|default|unlimited|custom` управляет `readBuf` / `maxDCBuf` / mem-limit.

---

## 12. Где курить дальше

- Структурные проблемы: **§4** (VP8 reliability) + **§7.1** (close race) + **§7.3** (no half-close) + **§7.4** (DC inCh drop).
- Архитектурный выбор: **§5** (DC mode уже есть, не измерен в relay-only).
- Конкретные файлы для рефакторинга lifecycle: `tunnel/relay_bridge.go::socksConn.*` и `creator/session.go::connectTCP/closeDCConn`.
- Бенчмарки в первую очередь: один большой curl 92 MB в `dc` mode при `relay` policy → если завершается на 100 %, дальнейший ARQ-effort избыточен.

См. сопровождающий план: `docs/TRANSPORT_FIX_PLAN.md`.
